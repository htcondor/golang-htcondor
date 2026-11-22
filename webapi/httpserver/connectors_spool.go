package httpserver

import (
	"context"
	"fmt"
	"io"
	"net/http"

	"github.com/PelicanPlatform/classad/classad"

	"github.com/htcondor/golang-htcondor/logging"
	"github.com/htcondor/golang-htcondor/webapi/connectors"
)

// Putting a job's connector files into its spool.
//
// SpoolJobFilesFromTar sends what the stream holds and then releases
// the job, so the connector files and the user's upload have to be in
// ONE stream. The refs travel from submit to here on the job ad
// (connectors.InputAttribute), and this merges them into the upload as
// it goes past.
//
// Streaming throughout: the connector file is opened only when the tar
// writer is ready for its bytes, and nothing is buffered here. The
// cluster path's own bounded buffer (spool.Growing) is fed the merged
// stream rather than the raw one, so one fetch serves every proc
// instead of one fetch per proc.

// connectorUploadReader returns upload with the job's connector files
// merged in, or upload unchanged when it has none.
//
// The returned reader must be consumed; abandoning it leaks the
// goroutine feeding the pipe until its writes fail.
func (s *Handler) connectorUploadReader(
	ctx context.Context,
	r *http.Request,
	ad *classad.ClassAd,
	upload io.Reader,
) (io.Reader, error) {
	refs, err := connectorRefsOf(ad)
	if err != nil {
		return nil, err
	}
	if len(refs) == 0 {
		return upload, nil
	}

	session, ok := s.connectCaller(r)
	if !ok || session.Username == "" {
		return nil, fmt.Errorf("this job needs connector files, which only the browser " +
			"session that owns them can supply")
	}

	files, err := s.spoolFilesFor(ctx, r, session, refs)
	if err != nil {
		return nil, err
	}

	pr, pw := io.Pipe()
	go func() {
		// MergeSpoolTarToPipe owns the close, including closing WITH
		// the error on failure -- a plain close would hand the schedd
		// a well-formed archive holding a prefix of the files and it
		// would release the jobs with a partial sandbox.
		_ = connectors.MergeSpoolTarToPipe(pw, upload, files)
	}()
	return pr, nil
}

// connectorRefsOf reads the refs a submission recorded on its job ad.
func connectorRefsOf(ad *classad.ClassAd) ([]connectors.InputRef, error) {
	if ad == nil {
		return nil, nil
	}
	value, ok := ad.EvaluateAttrString(connectors.InputAttribute)
	if !ok || value == "" {
		return nil, nil
	}
	refs, err := connectors.DecodeInputRefs(value)
	if err != nil {
		return nil, fmt.Errorf("this job's %s is unreadable: %w", connectors.InputAttribute, err)
	}
	return refs, nil
}

// spoolFilesFor turns refs into fetchable spool entries.
//
// One token per connector rather than one per file, and the size is
// read first because tar writes it before the body -- which is what
// keeps this streaming rather than buffering a file to measure it.
func (s *Handler) spoolFilesFor(
	ctx context.Context,
	r *http.Request,
	session *SessionData,
	refs []connectors.InputRef,
) ([]connectors.SpoolFile, error) {
	tokens := map[string]string{}
	defs := map[string]connectors.Definition{}

	out := make([]connectors.SpoolFile, 0, len(refs))
	for _, ref := range refs {
		def, ok := defs[ref.Service]
		if !ok {
			found, err := s.connectorByService(ref.Service)
			if err != nil {
				return nil, err
			}
			// Group membership is checked HERE, at use, against
			// current membership -- not against a list frozen when the
			// job was submitted. A user removed from the group between
			// submit and upload gets no files, which is the half that
			// makes removing somebody from a group take something
			// away.
			if !found.Permits(session.Groups) {
				return nil, fmt.Errorf("connector %q is not available to you", found.ID)
			}
			def, defs[ref.Service] = found, found
		}

		token, ok := tokens[ref.Service]
		if !ok {
			t, err := s.connectorAccessToken(ctx, r, def, session.Username)
			if err != nil {
				return nil, err
			}
			token, tokens[ref.Service] = t, t
		}

		drive := &connectors.Drive{BaseURL: s.connectorDriveBaseURL}
		meta, err := drive.Stat(ctx, token, ref.FileID)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ref.Name, err)
		}

		out = append(out, connectors.SpoolFile{
			Name:      ref.Name,
			Size:      meta.Size,
			SizeKnown: meta.SizeKnown,
			// Opened only when the tar writer wants its bytes, so a
			// submission with several large inputs holds one stream at
			// a time rather than all of them.
			Open: func() (io.ReadCloser, error) {
				return drive.Open(ctx, token, ref.FileID)
			},
		})
	}

	for service := range defs {
		def := defs[service]
		if store := s.connectorStore(); store != nil {
			if err := store.MarkUsed(ctx, session.Username, def.ID); err != nil {
				s.logger.Warn(logging.DestinationHTTP, "could not record connector use",
					"connector", def.ID.String(), "user", session.Username, "error", err)
			}
		}
	}
	return out, nil
}

// connectorByService finds the connector a spool reference names.
func (s *Handler) connectorByService(service string) (connectors.Definition, error) {
	for _, d := range s.connectorDefinitions() {
		if d.Service == service && d.Enabled {
			return d, nil
		}
	}
	return connectors.Definition{}, fmt.Errorf(
		"this job names connector files from %q, which this access point does not configure",
		service)
}

// connectorAccessToken mints a token for one connector and user.
func (s *Handler) connectorAccessToken(
	ctx context.Context,
	r *http.Request,
	def connectors.Definition,
	user string,
) (string, error) {
	store := s.connectorStore()
	if store == nil {
		return "", fmt.Errorf("no credential store is configured")
	}
	refresh, err := store.RefreshToken(ctx, user, def.ID)
	if err != nil {
		return "", fmt.Errorf("you have not connected %s; visit %s",
			def.ID, connectURL(def.ID))
	}
	secret, err := s.connectorSecret(r, def)
	if err != nil {
		return "", fmt.Errorf("connector %s is not usable: %w", def.ID, err)
	}
	tok, err := s.connectorFlow().Refresh(ctx, def, secret, string(refresh))
	if err != nil {
		return "", fmt.Errorf("%s: %w", def.ID, err)
	}
	return tok.Token, nil
}

// prepareConnectorInputs takes the connector references out of a submit
// file and records them on the job instead.
//
// The reference cannot stay where it is: nothing advertises a
// a connector transfer method, so the schedd would add a
// TARGET.HasFileTransferPluginMethods clause nothing satisfies and the
// job would sit idle for ever with no error to explain it. So each is
// rewritten to the name its spooled copy will have, and the originals
// ride on the job ad for the upload to find.
func (s *Handler) prepareConnectorInputs(submitFile string) (string, error) {
	rewritten, refs, err := connectors.SplitSpoolInputs(submitFile)
	if err != nil {
		return "", err
	}
	if len(refs) == 0 {
		return submitFile, nil
	}

	// An output destination would need a transfer plugin on the
	// execute node, which is the direct path, not this one. Refusing
	// beats leaving the URL in place and letting the job never match.
	if outs := connectors.OutputRefs(submitFile); len(outs) > 0 {
		return "", fmt.Errorf("a connector cannot receive output in spool mode: %s. "+
			"Let the job write the file normally and push it afterwards", outs[0])
	}

	return connectors.RecordInputRefs(rewritten, refs), nil
}
