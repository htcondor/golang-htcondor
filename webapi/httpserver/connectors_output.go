package httpserver

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// Appending finished jobs' results to the spreadsheets their
// submissions nominated.
//
// This is the half that spends a credential with nobody watching. At
// job completion there is no browser session, so the token is minted
// from the owner's stored grant -- and the only thing that makes that
// something other than a general capability is the binding row, which
// only a session can create. Every write here is to a (spreadsheet,
// tab) some session nominated, for a cluster that session submitted.
//
// The sweep's dependencies are injected rather than reached for. Not
// for ceremony: the decision this makes per job -- append, account for,
// or leave alone -- is the part that must be right, and it is the part
// that a test cannot reach through a schedd.

// outputResultMaxBytes bounds one job's result file.
//
// A job can write a gigabyte. The point of failure should be the read,
// not the memory of the server doing the appending.
const outputResultMaxBytes = 4 << 20

// clusterState is what the queue says about one submission.
type clusterState struct {
	// Completed is the procs that have finished, in any order.
	Completed []int
	// Unfinished is how many of the cluster's jobs could still produce a
	// result: anything neither completed nor removed. Zero means the
	// submission is over, whether because every job finished or because
	// the cluster has left the queue entirely.
	Unfinished int
	// Present reports whether the cluster is still in the queue at all.
	// Kept apart from Unfinished because they answer different
	// questions and only one of them is about whether to look again.
	Present bool
}

// done reports whether this submission can produce nothing further.
func (c clusterState) done() bool { return c.Unfinished == 0 }

// outputSweeper appends what finished jobs produced.
type outputSweeper struct {
	store  *connectors.Store
	sheets *connectors.Sheets
	logger *logging.Logger

	// cluster reports which of a submission's jobs have finished.
	cluster func(ctx context.Context, b connectors.OutputBinding) (clusterState, error)
	// result opens one job's result file. The caller closes it.
	result func(ctx context.Context, b connectors.OutputBinding, proc int) (io.ReadCloser, error)
	// token mints an access token for the binding's owner.
	token func(ctx context.Context, b connectors.OutputBinding) (string, error)

	maxBytes int64
}

func (w *outputSweeper) limit() int64 {
	if w.maxBytes > 0 {
		return w.maxBytes
	}
	return outputResultMaxBytes
}

// Sweep does one pass over every open binding.
//
// One binding's failure does not stop the others: they belong to
// different people and a provider refusing one account says nothing
// about the next.
func (w *outputSweeper) Sweep(ctx context.Context) {
	bindings, err := w.store.OpenOutputBindings(ctx, 0)
	if err != nil {
		w.warn("could not list output bindings", "error", err)
		return
	}
	for _, b := range bindings {
		if err := ctx.Err(); err != nil {
			return
		}
		if err := w.sweepBinding(ctx, b); err != nil {
			w.warn("could not add a submission's results to its spreadsheet",
				"cluster", b.Cluster, "subject", b.Subject,
				"spreadsheet", b.FileID, "tab", b.Tab, "error", err)
		}
	}
}

// sweepBinding appends every finished job of one submission that has
// not been accounted for yet.
func (w *outputSweeper) sweepBinding(ctx context.Context, b connectors.OutputBinding) error {
	state, err := w.cluster(ctx, b)
	if err != nil {
		return fmt.Errorf("asking the queue about cluster %d: %w", b.Cluster, err)
	}

	done, err := w.store.AppendedJobs(ctx, b.Subject, b.Schedd, b.Cluster)
	if err != nil {
		return err
	}

	pending := make([]int, 0, len(state.Completed))
	for _, proc := range state.Completed {
		if _, already := done[proc]; !already {
			pending = append(pending, proc)
		}
	}
	// In proc order, so a spreadsheet read top to bottom follows the
	// submission rather than whatever order a query returned.
	sort.Ints(pending)

	if len(pending) == 0 {
		// Nothing outstanding, so the binding is finished if nothing
		// more can arrive -- every job accounted for, or the cluster
		// gone from the queue. Retiring it is what stops a reused
		// cluster id from inheriting this destination, and it is also
		// what stops the sweep from asking about a finished submission
		// for the rest of its ten days in the queue: a cluster of a
		// hundred thousand completed jobs would otherwise be re-read
		// every pass to learn nothing.
		if state.done() {
			return w.store.RetireOutputBinding(ctx, b.Subject, b.Schedd, b.Cluster)
		}
		return nil
	}

	token, err := w.token(ctx, b)
	if err != nil {
		return fmt.Errorf("no usable credential for %s: %w", b.Connector, err)
	}

	// The header row, read once per sweep and re-read every sweep. Once
	// because a read per job would be a read per job; again each time
	// because a column added to the spreadsheet should start being
	// filled without anybody resubmitting.
	headers, err := w.headers(ctx, token, b)
	if err != nil {
		return err
	}
	if len(headers) == 0 {
		// Nothing is accounted for: the person can add a header row and
		// the next sweep will fill it. Marking these jobs would be
		// throwing their results away to make the warning stop.
		return fmt.Errorf("the tab %q of spreadsheet %s has no header row, so there is "+
			"nowhere to put a result; add one and these jobs will be added on the next pass",
			b.Tab, b.FileID)
	}

	for _, proc := range pending {
		if err := ctx.Err(); err != nil {
			return err
		}
		rows, note := w.readRows(ctx, b, proc)
		if rows == nil {
			// Accounted for with the reason rather than retried for
			// ever: a job whose result file is missing or unreadable
			// will not become readable.
			if err := w.store.MarkAppended(ctx, b.Subject, b.Schedd, b.Cluster,
				connectors.AppendedJob{Proc: proc, Rows: 0, Note: note}); err != nil {
				return err
			}
			w.warn("a job produced no usable result",
				"job", fmt.Sprintf("%d.%d", b.Cluster, proc), "reason", note)
			continue
		}

		cells, unused := connectors.FitToHeaders(headers, rows)
		if err := w.sheets.AppendRows(ctx, token, b.FileID, b.Tab, cells); err != nil {
			// Nothing is marked, so the next sweep tries again. An
			// append that failed added nothing, which is the one case
			// where retrying cannot duplicate.
			return fmt.Errorf("appending job %d.%d: %w", b.Cluster, proc, err)
		}

		if len(unused) > 0 {
			note = fmt.Sprintf("no column for %s", strings.Join(unused, ", "))
			w.warn("a job produced fields the spreadsheet has no column for",
				"job", fmt.Sprintf("%d.%d", b.Cluster, proc),
				"fields", strings.Join(unused, ","), "tab", b.Tab)
		}
		// AFTER the append. The other order loses results on a crash
		// between the two, and a missing row can be put right by hand
		// where a duplicate cannot.
		if err := w.store.MarkAppended(ctx, b.Subject, b.Schedd, b.Cluster,
			connectors.AppendedJob{Proc: proc, Rows: len(cells), Note: note}); err != nil {
			return err
		}
	}

	if state.done() {
		return w.store.RetireOutputBinding(ctx, b.Subject, b.Schedd, b.Cluster)
	}
	return nil
}

// headers reads the tab's first row.
func (w *outputSweeper) headers(
	ctx context.Context,
	token string,
	b connectors.OutputBinding,
) ([]string, error) {
	vals, err := w.sheets.Values(ctx, token, b.FileID, b.Tab, 1)
	if err != nil {
		return nil, fmt.Errorf("reading the header row of %q in %s: %w", b.Tab, b.FileID, err)
	}
	if len(vals.Rows) == 0 {
		return nil, nil
	}
	return vals.Rows[0], nil
}

// readRows reads and parses one job's result file.
//
// Returns nil rows and a reason when the job has nothing usable, which
// is a thing to account for rather than an error to retry.
func (w *outputSweeper) readRows(
	ctx context.Context,
	b connectors.OutputBinding,
	proc int,
) ([]connectors.ResultRow, string) {
	rc, err := w.result(ctx, b, proc)
	if err != nil {
		return nil, fmt.Sprintf("%s could not be read from the job's sandbox: %v", b.JobFile, err)
	}
	defer func() { _ = rc.Close() }()

	rows, err := connectors.ParseJobResult(rc, w.limit())
	if err != nil {
		return nil, fmt.Sprintf("%s could not be read as a result: %v", b.JobFile, err)
	}
	if len(rows) == 0 {
		return nil, fmt.Sprintf("%s held no records", b.JobFile)
	}
	return rows, ""
}

func (w *outputSweeper) warn(msg string, args ...any) {
	if w.logger == nil {
		return
	}
	w.logger.Warn(logging.DestinationHTTP, msg, args...)
}
