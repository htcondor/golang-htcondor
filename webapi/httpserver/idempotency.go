package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/PelicanPlatform/classad/classad"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/webapi/idempotency"
	"github.com/htcondor/golang-htcondor/webapi/multiap"
)

// IdempotencyKeyHeader is the request header carrying a submission's
// idempotency key.
//
// This spelling, rather than a field in the JSON body: it is what the
// IETF draft and every API that has this feature use, so a client's
// existing retry middleware already sets it; it applies to the
// multipart and form-shaped submit surfaces that have no JSON body to
// put it in; and a proxy or gateway can see it without parsing a body.
const IdempotencyKeyHeader = "Idempotency-Key"

// idempotencyKeyFrom returns the key a request supplied, and whether the
// header was present at all.
//
// A header present but empty is NOT the same as absent: a client that
// set it from a variable that turned out to be empty asked for the
// guarantee and would silently not get it. That is reported as an error
// by the Guard, so this only has to tell the two apart.
func idempotencyKeyFrom(r *http.Request) (string, bool) {
	vals := r.Header.Values(IdempotencyKeyHeader)
	if len(vals) == 0 {
		return "", false
	}
	// Two different keys for one submission is a client bug with no safe
	// reading, and taking the first would pick one arbitrarily. Join them
	// so the Guard refuses a key that cannot validate rather than
	// guessing; a repeated header with one value is the ordinary case and
	// still works.
	if len(vals) > 1 {
		key := vals[0]
		for _, v := range vals[1:] {
			if v != key {
				return "", true
			}
		}
	}
	return vals[0], true
}

// withIdempotencyKey puts the request's key on ctx for the submit path.
// It returns an error for a header that was sent but carries nothing
// usable, so the surface can answer 400 rather than submit unguarded.
func withIdempotencyKey(ctx context.Context, r *http.Request) (context.Context, error) {
	key, present := idempotencyKeyFrom(r)
	if !present {
		return ctx, nil
	}
	if key == "" {
		return ctx, &idempotency.KeyError{Reason: fmt.Sprintf(
			"the %s header is present but empty, or was sent more than once with different "+
				"values; a submission that asked to be idempotent is refused rather than "+
				"submitted unguarded", IdempotencyKeyHeader)}
	}
	return idempotency.WithKey(ctx, key), nil
}

// writeIdempotencyFailure answers an idempotency refusal, and reports
// whether err was one.
//
// The three statuses are three different things for a client to do, and
// collapsing them would make the feature unusable: 409 means the work is
// already queued and the retry succeeded in the sense the client cares
// about; 400 means the request is wrong and retrying it will not help;
// 503 means the check could not be made and the same request may well
// work later.
func (s *Handler) writeIdempotencyFailure(w http.ResponseWriter, err error) bool {
	var dup *idempotency.DuplicateError
	if errors.As(err, &dup) {
		s.writeJSON(w, http.StatusConflict, map[string]any{
			"error":           dup.Error(),
			"idempotency_key": dup.Key,
			"job_id":          dup.JobID,
		})
		return true
	}
	var unver *idempotency.UnverifiableError
	if errors.As(err, &unver) {
		s.writeError(w, http.StatusServiceUnavailable, unver.Error())
		return true
	}
	var bad *idempotency.KeyError
	if errors.As(err, &bad) {
		s.writeError(w, http.StatusBadRequest, bad.Error())
		return true
	}
	return false
}

// idemLookups reads the live queue and the completed-job history for the
// Guard.
type idemLookups struct{ h *Handler }

// Queue reads the schedd itself, not the mirror's jobs table. See the
// idempotency package comment: the mirror's lag is seconds and a retry
// arrives in seconds, so a mirrored read is least trustworthy exactly
// when this asks.
func (l idemLookups) Queue(ctx context.Context, constraint string, attrs []string, limit int) ([]*classad.ClassAd, error) {
	schedd := l.h.scheddFor(ctx)
	if schedd == nil {
		return nil, errors.New("no schedd to query")
	}
	// FetchIncludeClusterAd, because the key lives on the CLUSTER ad.
	// A query that returns only proc ads still finds it -- the schedd
	// merges a proc ad with its chained cluster ad before answering --
	// but it finds NOTHING for a cluster whose procs have not
	// materialized yet, which is a late-materialization factory in the
	// window between the submit and the first proc. Matching the cluster
	// ad covers that, and costs nothing extra at limit 1.
	ads, _, err := schedd.QueryWithOptions(ctx, constraint, &htcondor.QueryOptions{
		Limit:      limit,
		Projection: attrs,
		FetchOpts:  htcondor.FetchIncludeClusterAd,
	})
	return ads, err
}

// History reads the history table of the htcondordb mirror.
func (l idemLookups) History(ctx context.Context, constraint string, attrs []string, limit int) ([]*classad.ClassAd, error) {
	if !l.HistoryConfigured() {
		return nil, idempotency.ErrHistoryUnavailable
	}
	dbc, closer, _, err := l.h.dbMirror.Client(ctx)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", idempotency.ErrHistoryUnavailable, err)
	}
	defer closer()

	// QueryRawProject and not ArchiveQuery: the projection op resolves a
	// mutable table, a view's backing and an append-only archive
	// uniformly (an archive streams newest-first, as its own query
	// does), so this one call serves whichever shape the history table
	// has -- and it answers in OLD-ClassAd text either way, which is why
	// ParseOld below is right. The two serializations are a real trap:
	// each parser reads the other's output as an empty ad with NO error,
	// so a mismatch here would report every key as unused.
	rows, err := dbc.QueryRawProject(ctx, multiap.TableHistory, constraint, attrs, limit)
	if err != nil {
		return nil, err
	}
	ads := make([]*classad.ClassAd, 0, len(rows))
	for _, row := range rows {
		ad, perr := classad.ParseOld(row)
		if perr != nil {
			// Unlike a dashboard, this cannot skip what it cannot read.
			// A row came back, so the key IS used; dropping the row
			// would report it unused and let a duplicate submission
			// through. Fail the lookup and let ModeFull refuse.
			return nil, fmt.Errorf("a matching history record could not be parsed: %w", perr)
		}
		ads = append(ads, ad)
	}
	return ads, nil
}

// HistoryConfigured reports whether a mirror is configured to read.
func (l idemLookups) HistoryConfigured() bool {
	return l.h != nil && l.h.dbMirror != nil && l.h.dbMirror.Enabled()
}
