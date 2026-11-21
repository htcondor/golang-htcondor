package connectors

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"sort"
)

// Merging connector files into the tar a user uploads.
//
// SpoolJobFilesFromTar sends what the stream holds and then releases
// the job, so the connector files and the user's own have to be in ONE
// stream. Two tars cannot simply be concatenated -- each ends with its
// own trailer -- so the user's entries are copied into a new archive
// alongside the fetched ones.

// SpoolFile is one file to add to a spool.
//
// Open rather than bytes, so a large input is streamed rather than held
// in memory while a job's whole spool is assembled.
type SpoolFile struct {
	Name string
	Size int64
	// SizeKnown reports that Size came from the provider rather than
	// being a zero nobody filled in.
	//
	// tar writes the size in the header BEFORE the body, so a file
	// whose size the provider does not report cannot be streamed into
	// one -- and the only alternatives are buffering it, which this
	// path exists to avoid, or writing a header that lies. Drive does
	// not report a size for its own document types, so this is a real
	// case and not a defensive flourish.
	SizeKnown bool
	Open      func() (io.ReadCloser, error)
}

// MergeSpoolTar writes userTar's entries and extra into one archive.
//
// A name in extra that the user also uploaded is an error rather than
// an overwrite: whichever is written last would win, and a job running
// on the wrong input is worse than one that does not start. The
// submit-time check refuses this too; it is checked again here because
// this is the point at which it would actually happen, and the two
// checks see different things -- that one reads the submit file, this
// one sees what was really uploaded.
func MergeSpoolTar(w io.Writer, userTar io.Reader, extra []SpoolFile) error {
	byName := make(map[string]SpoolFile, len(extra))
	for _, f := range extra {
		if _, dup := byName[f.Name]; dup {
			return fmt.Errorf("two connector files are both called %q", f.Name)
		}
		byName[f.Name] = f
	}

	tw := tar.NewWriter(w)
	tr := tar.NewReader(userTar)

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading the uploaded archive: %w", err)
		}
		if _, clash := byName[hdr.Name]; clash {
			return fmt.Errorf("%q was uploaded and is also a connector file; "+
				"one would overwrite the other", hdr.Name)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return fmt.Errorf("writing %q: %w", hdr.Name, err)
		}
		//nolint:gosec // G110: the caller bounds the upload; see the size
		// cap on the request body in handleJobInput.
		if _, err := io.Copy(tw, tr); err != nil {
			return fmt.Errorf("copying %q: %w", hdr.Name, err)
		}
	}

	// Sorted, so one submission produces one archive whatever order the
	// fetches finished in.
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		f := byName[name]
		if err := writeSpoolFile(tw, f); err != nil {
			return err
		}
	}
	return tw.Close()
}

func writeSpoolFile(tw *tar.Writer, f SpoolFile) error {
	if !f.SizeKnown {
		return fmt.Errorf("%q: the provider reported no size, and a file has to be "+
			"sized before it can be streamed into an archive. Google reports no size "+
			"for its own document types (Docs, Sheets, Slides), which have to be "+
			"exported to a chosen format rather than downloaded", f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("fetching %q: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()

	if err := tw.WriteHeader(&tar.Header{
		Name:     f.Name,
		Mode:     0o600,
		Size:     f.Size,
		Typeflag: tar.TypeReg,
	}); err != nil {
		return fmt.Errorf("writing the header for %q: %w", f.Name, err)
	}

	n, err := io.Copy(tw, rc)
	if err != nil {
		return fmt.Errorf("copying %q: %w", f.Name, err)
	}
	// tar declares a size up front, so a provider that returned fewer
	// bytes than it advertised would otherwise produce a truncated
	// entry that looks complete. Caught here rather than leaving the
	// job to fail on a short input file.
	if n != f.Size {
		return fmt.Errorf("%q: the provider sent %d bytes, not the %d it declared",
			f.Name, n, f.Size)
	}
	return nil
}

// MergeSpoolTarToPipe is MergeSpoolTar writing into a pipe, and it
// closes the pipe itself.
//
// Use THIS rather than MergeSpoolTar with a pipe of your own. The
// closing discipline is the whole reason it exists, and this repository
// has already paid for getting it wrong once: tar.Writer.Close appends
// a valid end-of-archive marker to whatever has been written so far, so
// a failure followed by a plain close hands the reader a well-formed
// archive holding a PREFIX of the files. The schedd accepts it and
// releases the jobs; the 500 the handler returns afterwards arrives too
// late, and every proc of the cluster is running with a partial
// sandbox. See writeMultipartTar, which carries the same note for the
// same reason.
//
// So: on success the marker goes on and the pipe closes cleanly. On
// failure the pipe is closed WITH the error, and the reader sees an
// error rather than a shorter archive.
func MergeSpoolTarToPipe(pw *io.PipeWriter, userTar io.Reader, extra []SpoolFile) (err error) {
	defer func() {
		if err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = pw.Close()
	}()
	return MergeSpoolTar(pw, userTar, extra)
}
