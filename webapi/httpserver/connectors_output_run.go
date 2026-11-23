package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	htcondor "github.com/htcondor/golang-htcondor"
	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/connectors"
	"github.com/htcondor/golang-htcondor/webapi/ownerscope"
)

// Giving the output sweep its three seams, and something that runs it.
//
// outputSweeper decides, per finished job, whether to append, account
// for, or leave alone. That decision is in connectors_output.go and is
// tested without a schedd. This file is the other half: where the
// answers come from on a real access point, and what the sweep is
// permitted to do while getting them.
//
// The whole of this file's security story is that the sweep acts AS the
// binding's owner, never as this daemon. A daemon credential on the
// local schedd is a queue superuser, so a sweep holding one would be a
// way to read any job's sandbox on the machine -- and the thing that
// drives it is a row in a table. Minting a short-lived token for the
// owner instead (actAsOwner) keeps a binding's reach to the account
// that created it, which is the same account whose spreadsheet the
// results go to.

// outputSweepAuthz is what the sweep's minted credential carries.
//
// READ to ask the queue which jobs finished, WRITE because retrieving a
// sandbox stamps StageOutStart/StageOutFinish on the job ad -- the same
// pair a shared output URL needs (shareurl.NeededAuthz). Not
// ADMINISTRATOR, CONFIG, DAEMON or NEGOTIATOR, for the reason
// superuserAuthz gives: the extra levels buy nothing here and would
// make a leaked credential worth stealing.
var outputSweepAuthz = []string{"READ", "WRITE"}

// outputActLifetime bounds one binding's acting credential.
//
// Longer than a share redemption's because one binding's turn makes a
// queue query and then a sandbox read per finished job, and they share
// the credential: re-minting per read would re-authenticate to the
// schedd per read. A sweep that somehow outlives it fails that binding
// with an expired-credential error and the next one picks it up, which
// is why this is a bound and not a deadline.
const outputActLifetime = 10 * time.Minute

// newOutputSweeper builds the sweeper with its real dependencies.
//
// The acting context is memoized for the life of one Sweep, keyed by
// binding: one mint and one authenticated schedd session per
// submission, rather than per job.
func (h *Handler) newOutputSweeper() *outputSweeper {
	acting := map[string]context.Context{}

	actFor := func(ctx context.Context, b connectors.OutputBinding) (context.Context, error) {
		key := fmt.Sprintf("%s\x1f%s\x1f%d", b.Subject, b.Schedd, b.Cluster)
		if c, ok := acting[key]; ok {
			return c, nil
		}
		c, err := h.actAsOwner(ctx, b.Subject, outputSweepAuthz, outputActLifetime)
		if err != nil {
			return nil, err
		}
		acting[key] = c
		return c, nil
	}

	return &outputSweeper{
		store:  h.connectorStore(),
		sheets: &connectors.Sheets{BaseURL: h.connectorSheetsBaseURL},
		logger: h.logger,
		cluster: func(ctx context.Context, b connectors.OutputBinding) (clusterState, error) {
			c, err := actFor(ctx, b)
			if err != nil {
				return clusterState{}, err
			}
			return h.clusterStateFor(c, b)
		},
		result: func(ctx context.Context, b connectors.OutputBinding, proc int) (io.ReadCloser, error) {
			c, err := actFor(ctx, b)
			if err != nil {
				return nil, err
			}
			return h.outputResultFile(c, b, proc)
		},
		token: h.outputConnectorToken,
	}
}

// clusterStateFor asks the queue which of a submission's jobs are done.
//
// Scoped to the binding's owner, not to the cluster alone. A binding
// names a cluster id, and cluster ids are reused: retiring a binding
// when its cluster leaves the queue makes a collision unlikely, and the
// failure it would otherwise produce -- one person's job results
// appended to another person's spreadsheet -- is bad enough to be worth
// a clause that costs nothing. ownerscope.Constrain builds it, so the
// two operands are ANDed through the ClassAd parser rather than spliced.
func (h *Handler) clusterStateFor(
	ctx context.Context,
	b connectors.OutputBinding,
) (clusterState, error) {
	constraint, err := outputClusterConstraint(b)
	if err != nil {
		return clusterState{}, err
	}

	// Unlimited, explicitly. The default limit is 50, and a submission
	// bigger than that would have its later procs silently never seen
	// as finished -- the sweep would appear to work and would lose most
	// of a batch's results. Two attributes per job keeps the cost of
	// saying so small, and a binding retires as soon as everything is
	// accounted for, so a big finished cluster is read once and not
	// again.
	ads, _, err := h.scheddFor(ctx).QueryWithOptions(ctx, constraint, &htcondor.QueryOptions{
		Limit:      -1,
		Projection: []string{"ProcId", "JobStatus"},
	})
	if err != nil {
		return clusterState{}, err
	}

	state := clusterState{Present: len(ads) > 0}
	for _, ad := range ads {
		status, ok := ad.EvaluateAttrInt("JobStatus")
		if !ok {
			// A job whose status cannot be read is counted as still to
			// come rather than skipped: the alternative is retiring a
			// binding because an attribute was missing once.
			state.Unfinished++
			continue
		}
		switch int(status) {
		case jobStatusCompleted:
			proc, ok := ad.EvaluateAttrInt("ProcId")
			if !ok {
				continue
			}
			state.Completed = append(state.Completed, int(proc))
		case jobStatusRemoved:
			// Removed jobs never produce a result, and waiting for one
			// would hold the binding open for the cluster's whole stay
			// in the queue.
		default:
			state.Unfinished++
		}
	}
	return state, nil
}

// outputClusterConstraint names one binding's jobs.
//
// Separate from the query because this is the part that has to be
// right: which attribute carries the owner, and that the owner clause
// cannot be escaped by the rest of the expression. The ClusterId
// operand is this server's own text, but it goes through the same AND
// as any untrusted one -- a second path that splices is a second path
// that can be got wrong later.
func outputClusterConstraint(b connectors.OutputBinding) (string, error) {
	attr, owner := "Owner", b.Subject
	if b.UserScoped {
		attr = "User"
	} else if i := strings.LastIndex(owner, "@"); i >= 0 {
		// The schedd stores Owner as the local part.
		owner = owner[:i]
	}
	c, err := ownerscope.Constrain(attr, owner, fmt.Sprintf("ClusterId == %d", b.Cluster))
	if err != nil {
		return "", fmt.Errorf("naming cluster %d's jobs: %w", b.Cluster, err)
	}
	return c, nil
}

// outputResultFile reads one job's declared result file out of its
// sandbox.
//
// fetchSandboxFileBytes rather than a stream, because it is already the
// bounded reader this needs: it stops at maxBytes and says whether it
// stopped. A truncated result is reported as an error rather than
// parsed, since a JSON document cut in half parses as a syntax error
// and "results.json is not valid JSON" would be a lie about why.
func (h *Handler) outputResultFile(
	ctx context.Context,
	b connectors.OutputBinding,
	proc int,
) (io.ReadCloser, error) {
	constraint := fmt.Sprintf("ClusterId == %d && ProcId == %d", b.Cluster, proc)
	data, truncated, err := h.fetchSandboxFileBytes(ctx, constraint, b.JobFile, outputResultMaxBytes)
	if err != nil {
		if errors.Is(err, errSandboxFileNotFound) {
			return nil, fmt.Errorf("the job did not produce %s; "+
				"a job's results file has to be named in transfer_output_files to come back", b.JobFile)
		}
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("%s is larger than the %d bytes this access point "+
			"will add to a spreadsheet", b.JobFile, outputResultMaxBytes)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

// outputConnectorToken mints the Google credential one binding writes
// with.
func (h *Handler) outputConnectorToken(
	ctx context.Context,
	b connectors.OutputBinding,
) (string, error) {
	def, ok := h.connectorDefinition(b.Connector)
	if !ok {
		return "", fmt.Errorf("connector %s is no longer configured", b.Connector)
	}
	if !def.Enabled {
		return "", fmt.Errorf("connector %s is disabled", b.Connector)
	}

	groups, answered := []string(nil), false
	if connectorGroupCheckNeeded(def) {
		groups, answered = h.liveGroupsFor(ctx, b.Subject)
	}
	allowed, note := connectorStillPermitted(def, groups, answered)
	if !allowed {
		return "", fmt.Errorf("%s", note)
	}
	if note != "" {
		w := &outputSweeper{logger: h.logger}
		w.warn("could not re-check group membership before writing on a user's behalf",
			"connector", def.ID.String(), "subject", b.Subject, "reason", note)
	}

	return h.connectorAccessToken(ctx, def, b.Subject)
}

// connectorGroupCheckNeeded reports whether this connector restricts by
// group at all. A connector open to everyone ("*") asks no question.
func connectorGroupCheckNeeded(def connectors.Definition) bool {
	return !slices.Contains(def.Groups, "*")
}

// connectorStillPermitted decides whether a binding may still spend its
// credential, given what could be learned about its owner's groups.
//
// Membership is re-checked at use rather than snapshotted, for the
// reason Definition.Permits gives: a snapshot catches an operator
// changing policy and misses a user being removed from a group, which
// is the half that matters for taking access away. The sweep has no
// session, so membership comes from the system (liveGroupsFor).
//
// The case that needed deciding is the one where NOTHING can report
// membership. This refused at first, on the argument that an operator
// who confined a connector to a group meant it. An integration test
// showed what that costs: liveGroupsFor answers only when this machine
// sources groups AND the identity is in UID_DOMAIN, so on an access
// point whose groups come from an identity provider -- which is most of
// them -- the sweep would never run at all, for anybody, and would say
// so once per binding per pass for ten days.
//
// So: a check that can be made is made, and a user who has genuinely
// lost membership is refused. A check that CANNOT be made proceeds and
// says so. "Could not ask" is not evidence of removal, and what is
// being allowed is narrow -- one tab, for one cluster, nominated by a
// session that did pass the check, for as long as that cluster stays in
// the queue. A deployment that sources groups locally still gets
// revocation; one that does not gets the feature.
//
// Returns allowed, and a note: the refusal when not allowed, and what
// could not be checked when allowed anyway.
func connectorStillPermitted(
	def connectors.Definition,
	groups []string,
	answered bool,
) (allowed bool, note string) {
	if !connectorGroupCheckNeeded(def) {
		return true, ""
	}
	if !answered {
		return true, fmt.Sprintf("connector %s is restricted to a group, and this server "+
			"cannot read the owner's membership without them signed in", def.ID)
	}
	if !def.Permits(groups) {
		return false, fmt.Sprintf("the owner is no longer in a group that may use connector %s",
			def.ID)
	}
	return true, ""
}

// startConnectorOutputSweeper runs the sweep on a timer.
//
// Off unless HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL is set: the sweep
// spends a user's Google credential with nobody watching, so it is an
// operator's decision to turn on and not a default.
//
// Refused outright in multi-AP mode. scheddFor there answers only from
// a routed request's context and fails closed without a caller
// credential, deliberately -- acting without one would run as this
// daemon on somebody else's access point. The sweep has no request, and
// manufacturing a route for it would be defeating that check rather
// than satisfying it. Single-AP is where this feature works today.
func (h *Handler) startConnectorOutputSweeper(ctx context.Context) {
	interval := h.connectorOutputSweepInterval
	if interval <= 0 {
		return
	}
	if h.multi != nil {
		h.logger.Warn(logging.DestinationHTTP,
			"Not starting the connector output sweep: it is not available in multi-AP mode",
			"parameter", "HTTP_API_CONNECTOR_OUTPUT_SWEEP_INTERVAL")
		return
	}
	if h.connectorStore() == nil {
		h.logger.Warn(logging.DestinationHTTP,
			"Not starting the connector output sweep: no application database is configured")
		return
	}

	h.wg.Add(1)
	go func() {
		defer h.wg.Done()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		h.logger.Info(logging.DestinationHTTP, "Started the connector output sweep",
			"interval", interval.String())

		for {
			select {
			case <-ticker.C:
				// A fresh sweeper per pass: its acting-credential memo
				// is per-pass by construction, so nothing holds a
				// minted token between passes.
				h.newOutputSweeper().Sweep(ctx)
			case <-ctx.Done():
				h.logger.Info(logging.DestinationHTTP, "Stopping the connector output sweep")
				return
			}
		}
	}()
}
