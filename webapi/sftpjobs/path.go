// Copyright 2026 Morgridge Institute for Research
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package sftpjobs serves a job surface over SFTP.
//
// SFTP is the only file protocol every user already has a client for --
// a shell, a file manager, rsync, a GUI on Windows -- so it is the
// lowest-friction way to hand somebody their job output. The tree is
// deliberately small:
//
//	/outputs/                      one directory per readable job
//	/outputs/<cluster>.<proc>/     that job: its ad and its output
//	/outputs/<cluster>.<proc>/job.ad    the job's ClassAd
//	/outputs/<cluster>.<proc>/data/     its output sandbox, as files
//	/outputs/<cluster>.<proc>/data.tar  the same sandbox as one archive
//	/templates/<id>/               a submit description to drop a CSV on
//	/templates/<id>/submit/        drop it here; one job per row
//	/templates/<id>/status/        what happened to each upload
//	/submit/                       drop a tarball holding a job.sub
//	/submit/status/                what happened to each one
//
// The output sandbox sits one level down, under data/, and that is what
// keeps a listing cheap. A spooled sandbox can only be read whole, so
// putting a size next to a file's name costs a transfer of the entire
// sandbox -- which is what `ls -lh` of a job directory used to do. With
// the files under data/, a job directory holds a directory (no size)
// and an ad (one query), so `ls -lh /outputs` and `ls -lh
// /outputs/<job>` are both cheap and the unavoidable pass happens when
// somebody actually asks for the output.
//
// data.tar is addressable but NOT listed, on purpose twice over.
// Listing it would mean knowing its length, which needs the job's
// layout -- the very transfer the level above avoids. And listed beside
// data/, `get -r` on the job directory would fetch every file twice,
// once loose and once inside the archive. A path a client names
// explicitly is allowed to be expensive; a listing is not.
package sftpjobs

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// The top-level directories.
//
// submitDir is the drop point for a whole submission: a tarball with a
// job.sub in it, for work no template describes. It is the same word as
// submitDirName inside a template directory, and deliberately so --
// "put it here to submit it" is one habit, whether what you are putting
// there is a CSV for somebody's template or a job of your own.
const (
	outputsDir   = "outputs"
	templatesDir = "templates"
	submitDir    = "submit"
)

// Names inside a template directory.
const (
	submitDirName = "submit"
	statusDirName = "status"
)

// Names inside a job directory.
//
// JobAdFileName is HTCondor's own name for a job's ad written as a
// file: the starter writes .job.ad into the execute directory, and
// `condor_q -l` prints the same thing. Keeping the name means somebody
// who has seen one knows what this is.
const (
	JobAdFileName = "job.ad"
	dataDirName   = "data"
	dataTarName   = dataDirName + ".tar"
)

type kind int

const (
	kindRoot kind = iota
	kindOutputs
	kindJobDir  // /outputs/<job>
	kindJobAd   // /outputs/<job>/job.ad
	kindJobData // /outputs/<job>/data
	kindJobFile // something inside data/
	kindJobTar  // /outputs/<job>/data.tar

	// The templates tree. Bounded by kindTemplates and kindDrop rather
	// than compared against directly; see inTemplates.
	kindTemplates    // /templates
	kindTemplate     // /templates/<id>
	kindTemplateFile // job.sub or README inside it
	kindSubmitDir    // .../submit
	kindSubmitFile   // .../submit/<name>
	kindStatusDir    // .../status
	kindStatusFile   // .../status/<name>

	// The tarball drop point. Last, so inDrop is the tail of the range.
	kindDrop           // /submit
	kindDropReadme     // /submit/README
	kindDropFile       // /submit/<name>
	kindDropStatusDir  // /submit/status
	kindDropStatusFile // /submit/status/<name>
)

// inTemplates and inDrop say which tree a kind belongs to.
//
// Predicates rather than the bare comparisons they replace: the
// templates kinds used to be everything from kindTemplates upwards, so
// adding a kind after them silently joined that tree -- the handlers
// would have dispatched a tarball drop into the template code and
// answered ENOENT for it.
func (k kind) inTemplates() bool { return k >= kindTemplates && k < kindDrop }
func (k kind) inDrop() bool      { return k >= kindDrop }

// ref is a parsed request path.
type ref struct {
	kind kind
	// cluster and proc are set for the job kinds.
	cluster, proc int
	// name is the sandbox-relative path for kindJobFile, and the file
	// name for kindTemplateFile, kindSubmitFile and kindStatusFile.
	name string
	// tid is the template's library id, set for every templates-tree
	// kind below kindTemplates.
	tid string
}

// parsePath reads a request path.
//
// The server hands over an absolute, cleaned path, but the components
// are checked anyway: this turns client input into a job id and a
// sandbox name, and a name that is not what it says must be refused
// rather than repaired.
func parsePath(p string) (ref, error) {
	parts := splitPath(p)
	if len(parts) == 0 {
		return ref{kind: kindRoot}, nil
	}
	switch parts[0] {
	case outputsDir:
	case templatesDir:
		return parseTemplatePath(p, parts[1:])
	case submitDir:
		return parseDropPath(p, parts[1:])
	default:
		return ref{}, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}
	if len(parts) == 1 {
		return ref{kind: kindOutputs}, nil
	}

	missing := func() (ref, error) { return ref{}, fmt.Errorf("%q: %w", p, fs.ErrNotExist) }

	cluster, proc, ok := parseJobID(parts[1])
	if !ok {
		return missing()
	}
	r := ref{cluster: cluster, proc: proc}
	if len(parts) == 2 {
		r.kind = kindJobDir
		return r, nil
	}

	switch parts[2] {
	case JobAdFileName:
		if len(parts) != 3 {
			return missing()
		}
		r.kind = kindJobAd
		return r, nil
	case dataTarName:
		if len(parts) != 3 {
			// Nothing lives under the archive.
			return missing()
		}
		r.kind = kindJobTar
		return r, nil
	case dataDirName:
		if len(parts) == 3 {
			r.kind = kindJobData
			return r, nil
		}
		r.kind = kindJobFile
		r.name = path.Join(parts[3:]...)
		return r, nil
	}
	return missing()
}

// parseTemplatePath reads what follows /templates.
//
// The namespace decides how many components name the template: a system
// template is <ns>/<name>, a user one is <ns>/<owner>/<name>. Anything
// deeper is one of the fixed names inside it, and anything else does not
// exist -- a template directory holds no tree of its own.
func parseTemplatePath(p string, rest []string) (ref, error) {
	missing := func() (ref, error) { return ref{}, fmt.Errorf("%q: %w", p, fs.ErrNotExist) }

	if len(rest) == 0 {
		return ref{kind: kindTemplates}, nil
	}
	id, tail := rest[0], rest[1:]
	if !plausibleTemplateID(id) {
		return missing()
	}

	switch len(tail) {
	case 0:
		return ref{kind: kindTemplate, tid: id}, nil
	case 1:
		switch tail[0] {
		case SubmitFileName, ReadmeFileName:
			return ref{kind: kindTemplateFile, tid: id, name: tail[0]}, nil
		case submitDirName:
			return ref{kind: kindSubmitDir, tid: id}, nil
		case statusDirName:
			return ref{kind: kindStatusDir, tid: id}, nil
		}
		return missing()
	case 2:
		// An upload's name is the client's, so it is checked rather than
		// trusted: it becomes a key and appears in a status listing.
		if checkUploadName(tail[1]) != nil {
			return missing()
		}
		switch tail[0] {
		case submitDirName:
			return ref{kind: kindSubmitFile, tid: id, name: tail[1]}, nil
		case statusDirName:
			return ref{kind: kindStatusFile, tid: id, name: tail[1]}, nil
		}
	}
	return missing()
}

// parseDropPath reads what follows /submit.
//
// Flat on purpose. A drop point with a tree would invite somebody to
// upload the pieces of a submission separately and wonder when it goes;
// one archive is the unit, and the only other thing here is where its
// outcome is written.
func parseDropPath(p string, rest []string) (ref, error) {
	missing := func() (ref, error) { return ref{}, fmt.Errorf("%q: %w", p, fs.ErrNotExist) }

	switch len(rest) {
	case 0:
		return ref{kind: kindDrop}, nil
	case 1:
		switch rest[0] {
		case ReadmeFileName:
			return ref{kind: kindDropReadme}, nil
		case statusDirName:
			return ref{kind: kindDropStatusDir}, nil
		}
		if checkUploadName(rest[0]) != nil {
			return missing()
		}
		return ref{kind: kindDropFile, name: rest[0]}, nil
	case 2:
		if rest[0] != statusDirName || checkUploadName(rest[1]) != nil {
			return missing()
		}
		return ref{kind: kindDropStatusFile, name: rest[1]}, nil
	}
	return missing()
}

// plausibleTemplateID bounds a component before it becomes a lookup key.
//
// Deliberately not a copy of the library's id rule: whether a template
// exists is the library's answer, and a second spelling of the pattern
// here could refuse an id the library would accept. This only keeps an
// absurd component from being carried around as a map key, and rules out
// the two names that are never an id.
func plausibleTemplateID(id string) bool {
	return id != "" && len(id) <= 64 && id != "." && id != ".."
}

// checkUploadName accepts a name for a dropped file.
//
// Narrow on purpose: the name is the client's, it keys the status entry
// somebody reads back, and it is shown in a listing. A name that is not
// one plain component is refused rather than reduced to one, because two
// uploads reducing to the same name would have one overwrite the
// other's result.
func checkUploadName(name string) error {
	if name == "" || len(name) > 128 {
		return errors.New("the name is empty or too long")
	}
	if name == "." || name == ".." || strings.HasPrefix(name, "-") {
		return fmt.Errorf("%q is not a usable file name", name)
	}
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9',
			r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("%q contains %q; use letters, digits, '.', '_' or '-'", name, r)
		}
	}
	return nil
}

// splitPath breaks a cleaned absolute path into components, refusing
// anything that is not a plain name.
func splitPath(p string) []string {
	var out []string
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			// A cleaned path has none, so this is either a client that
			// did not clean or a path assembled somewhere unexpected.
			// Either way it is not a name in this tree.
			return []string{"\x00"}
		}
		out = append(out, part)
	}
	return out
}

// jobDirName is how a job appears in /outputs.
func jobDirName(cluster, proc int) string {
	return strconv.Itoa(cluster) + "." + strconv.Itoa(proc)
}

// parseJobID accepts "N.M" and bare "N" (proc 0), with no sign, no
// padding and no spare parts -- the same spelling the SSH gateway's
// username target accepts, so one habit works in both places.
func parseJobID(s string) (cluster, proc int, ok bool) {
	clusterPart, procPart, hasProc := strings.Cut(s, ".")
	cluster, ok = parseIndex(clusterPart)
	if !ok {
		return 0, 0, false
	}
	if !hasProc {
		return cluster, 0, true
	}
	proc, ok = parseIndex(procPart)
	if !ok {
		return 0, 0, false
	}
	return cluster, proc, true
}

// parseIndex accepts only unsigned decimal digits: Atoi would take "+5"
// and " 5", which name a different job than the text does.
func parseIndex(s string) (int, bool) {
	if s == "" || len(s) > 18 {
		return 0, false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	return n, true
}
