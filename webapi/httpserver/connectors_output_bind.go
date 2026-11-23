package httpserver

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/connectors"
	"github.com/htcondor/golang-htcondor/webapi/ownerscope"
)

// Nominating where a submission's results go.
//
// This is the only moment at which an output binding can be created, and
// that is the point: the sweep writes to a user's spreadsheet with
// nobody present, and what keeps that from being a general capability
// over their account is that it may only touch a destination a browser
// session named, for a cluster that session submitted. The check that a
// session is really behind this request is therefore not a formality.
//
// Everything that can be refused is refused BEFORE the jobs are queued.
// After the submit the jobs exist, and a problem found then can only be
// reported as advice about a batch that is already running -- so a
// spreadsheet that cannot be written, or a results file that will never
// come back, is a 400 rather than ten thousand identical warnings.

// SubmitOutputSheet nominates a spreadsheet for a submission's results.
type SubmitOutputSheet struct {
	// Connector whose credential writes the rows, as "ns:name".
	Connector string `json:"connector"`
	// FileID is the spreadsheet, as the file chooser gave it.
	FileID string `json:"file_id"`
	// Tab is the worksheet within it, by title.
	Tab string `json:"tab"`
	// JobFile is the file in each job's sandbox holding its result,
	// JSON or CSV. One name for the whole cluster: every job of a
	// submission runs the same program.
	JobFile string `json:"job_file"`
}

// prepareOutputBinding validates a nominated destination and returns the
// binding to record once the cluster id is known.
//
// Returns a nil binding and no error when the request nominated nothing,
// which is the ordinary case.
//
// The warnings it returns are about things that are wrong but not
// fatal, and they ride on the submission's own 201 rather than being
// logged where the submitter will not see them.
func (s *Handler) prepareOutputBinding(
	ctx context.Context,
	r *http.Request,
	sheet *SubmitOutputSheet,
	submitFile string,
) (*connectors.OutputBinding, []string, error) {
	if sheet == nil {
		return nil, nil, nil
	}

	id, err := connectors.ParseID(strings.TrimSpace(sheet.Connector))
	if err != nil {
		return nil, nil, fmt.Errorf("output_sheet.connector %q is not a connector id: %w",
			sheet.Connector, err)
	}
	fileID := strings.TrimSpace(sheet.FileID)
	tab := strings.TrimSpace(sheet.Tab)
	jobFile := strings.TrimSpace(sheet.JobFile)
	if fileID == "" || tab == "" || jobFile == "" {
		return nil, nil, fmt.Errorf(
			"output_sheet needs a file_id, a tab and a job_file naming the file each job writes")
	}
	// A path would make the sweep look somewhere other than the
	// sandbox's top level, which is the only place it reads.
	if strings.ContainsAny(jobFile, `/\`) {
		return nil, nil, fmt.Errorf(
			"output_sheet.job_file is a file in the job's own directory, not a path: %q", jobFile)
	}

	// The session, not the token. Connector credentials are reachable
	// only from the browser session that holds them, and a binding is a
	// standing instruction to spend one later; a caller authenticated
	// some other way has no connector to bind.
	session, ok := s.connectCaller(r)
	if !ok || session.Username == "" {
		return nil, nil, fmt.Errorf("only the browser session that owns the connector " +
			"can nominate a spreadsheet for a submission's results")
	}

	def, ok := s.connectorDefinition(id)
	if !ok || !def.Enabled {
		return nil, nil, fmt.Errorf("connector %s is not available on this access point", id)
	}
	if !def.Permits(session.Groups) {
		return nil, nil, fmt.Errorf("connector %s is not available to you", id)
	}

	var warnings []string

	// Will the file even come back? A results file the job does not
	// transfer is the failure most likely to be made and least likely
	// to be noticed: the submission succeeds, the jobs run, and every
	// one of them is accounted for with the same reason.
	if w, err := outputFileComesBack(submitFile, jobFile); err != nil {
		return nil, nil, err
	} else if w != "" {
		warnings = append(warnings, w)
	}

	// Jobs that leave the queue the moment they finish cannot have their
	// results read: the sweep reads a completed job's sandbox, and a job
	// that is gone has none. SubmitRemote's own LeaveJobInQueue keeps
	// spooled jobs for ten days, but it defers to a submit file that
	// sets the command itself.
	if _, set := submitCommandValue(submitFile, "leave_in_queue"); set {
		warnings = append(warnings, "This submit file sets leave_in_queue, which overrides the "+
			"ten days completed jobs are normally kept for. Results can only be added to the "+
			"spreadsheet while a job is still in the queue, so some may be missed.")
	}

	// And can the spreadsheet be written? Asked here, once, rather than
	// discovered per job later.
	if err := s.checkSheetDestination(ctx, def, session.Username, fileID, tab); err != nil {
		return nil, nil, err
	}

	return &connectors.OutputBinding{
		Subject:   session.Username,
		Schedd:    routedSchedd(ctx),
		Connector: id,
		FileID:    fileID,
		Tab:       tab,
		JobFile:   jobFile,
		// What the submitting session is itself scoped by, so the sweep
		// names this cluster's jobs the same way the queue does for
		// this account. See migration 0046.
		UserScoped: ownerscope.UserScoped(ctx),
	}, warnings, nil
}

// checkSheetDestination confirms the tab exists and has a header row.
//
// The header row is the contract between a job's output and the
// spreadsheet's columns, so a tab without one has nowhere to put a
// result. The sweep handles that by accounting for nothing and waiting,
// which is right when a header is added later and useless as the answer
// to a submission that was never going to work.
func (s *Handler) checkSheetDestination(
	ctx context.Context,
	def connectors.Definition,
	user, fileID, tab string,
) error {
	token, err := s.connectorAccessToken(ctx, def, user)
	if err != nil {
		return err
	}
	sheets := &connectors.Sheets{BaseURL: s.connectorSheetsBaseURL}

	tabs, err := sheets.Tabs(ctx, token, fileID)
	if err != nil {
		return fmt.Errorf("that spreadsheet could not be read: %w", err)
	}
	found := false
	titles := make([]string, 0, len(tabs))
	for _, t := range tabs {
		titles = append(titles, t.Title)
		if t.Title == tab {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("that spreadsheet has no tab called %q; it has %s",
			tab, strings.Join(titles, ", "))
	}

	vals, err := sheets.Values(ctx, token, fileID, tab, 1)
	if err != nil {
		return fmt.Errorf("the tab %q could not be read: %w", tab, err)
	}
	if len(vals.Rows) == 0 || len(nonEmpty(vals.Rows[0])) == 0 {
		return fmt.Errorf("the tab %q has no header row, so there is nowhere to put a result; "+
			"put the column names in its first row", tab)
	}
	return nil
}

// recordOutputBinding stores the binding for a submission that is now in
// the queue.
//
// Returns a warning rather than an error. By the time this runs the jobs
// exist and are going to run; failing the submission would report a
// batch that is queued as not queued, which is worse than running it
// with nothing collecting the results.
func (s *Handler) recordOutputBinding(
	ctx context.Context,
	b *connectors.OutputBinding,
	cluster int,
) string {
	if b == nil {
		return ""
	}
	store := s.connectorStore()
	if store == nil {
		return "This access point has no application database, so results cannot be " +
			"added to a spreadsheet."
	}
	b.Cluster = cluster
	if err := store.PutOutputBinding(ctx, *b); err != nil {
		s.logger.Error(logging.DestinationHTTP, "could not record an output binding",
			"cluster", cluster, "subject", b.Subject, "spreadsheet", b.FileID, "error", err)
		return fmt.Sprintf("These jobs were submitted, but their results will not be added to "+
			"the spreadsheet: the destination could not be recorded (%v).", err)
	}
	if s.connectorOutputSweepInterval <= 0 {
		return "These jobs were submitted and the spreadsheet recorded, but this access point " +
			"is not configured to add results to spreadsheets, so nothing will be written."
	}
	return ""
}

// outputFileComesBack reports whether jobFile will be transferred back
// out of the job's sandbox.
//
// Three answers, not two. An absent transfer_output_files means HTCondor
// brings back everything new in the sandbox's top level, so the file
// comes back and there is nothing to say. A list naming the file is
// likewise fine. A list that does NOT name it is refused -- that
// submission cannot work, and refusing it is the only moment anyone is
// watching. A list that names something built from a macro is the third
// answer: it cannot be checked here, and a warning is honest where a
// refusal would be a guess.
func outputFileComesBack(submitFile, jobFile string) (warning string, err error) {
	raw, set := submitCommandValue(submitFile, "transfer_output_files")
	if !set {
		return "", nil
	}
	var named []string
	macro := false
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return r == ',' }) {
		f = strings.Trim(strings.TrimSpace(f), `"`)
		if f == "" {
			continue
		}
		if strings.Contains(f, "$(") {
			macro = true
			continue
		}
		named = append(named, f)
		if f == jobFile {
			return "", nil
		}
	}
	if macro {
		return fmt.Sprintf("This submit file's transfer_output_files is built from a macro, so "+
			"whether %q comes back could not be checked here. If it does not, every job will be "+
			"recorded as having produced no result.", jobFile), nil
	}
	return "", fmt.Errorf("these jobs do not transfer %s back, so there would be no result to "+
		"add to the spreadsheet; transfer_output_files names %s",
		jobFile, strings.Join(named, ", "))
}

// submitCommandValue reads the last value assigned to a submit command.
//
// Last wins: a submit file is a sequence of macro assignments and the
// one in force at `queue` is the last one written. The bool reports
// whether the command appeared at all, which is a different question
// from whether it has a value -- `transfer_output_files = ` with
// nothing after it transfers nothing, and is not the same as leaving it
// out.
func submitCommandValue(submitFile, key string) (string, bool) {
	value, found := "", false
	sc := bufio.NewScanner(strings.NewReader(submitFile))
	sc.Buffer(make([]byte, 0, 8*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexAny(line, "=:")
		if eq <= 0 {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(line[:eq]), key) {
			continue
		}
		value, found = strings.TrimSpace(line[eq+1:]), true
	}
	return value, found
}

// nonEmpty drops blank cells, for deciding whether a header row is one.
func nonEmpty(row []string) []string {
	out := make([]string, 0, len(row))
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	return out
}
