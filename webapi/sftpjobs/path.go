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
//	/outputs/                 one directory per readable job
//	/outputs/<cluster>.<proc>/   that job's sandbox
//	/outputs/<cluster>.<proc>.tar  the same sandbox as one archive
//
// The archive is addressable but NOT listed, which is the one surprising
// thing here and is on purpose twice over. Listing it would mean knowing
// its length, and a tar's length needs the job's layout -- so an `ls` of
// /outputs/ would drain every job's sandbox to answer. And if it were
// listed inside the job directory, `get -r` on that directory would
// fetch every file twice, once loose and once inside the archive. A path
// a client names explicitly is allowed to be expensive; a listing is
// not.
package sftpjobs

import (
	"fmt"
	"io/fs"
	"path"
	"strconv"
	"strings"
)

// outputsDir is the one top-level directory.
const outputsDir = "outputs"

// tarSuffix marks the archive form of a job.
const tarSuffix = ".tar"

type kind int

const (
	kindRoot kind = iota
	kindOutputs
	kindJobDir
	kindJobFile
	kindJobTar
)

// ref is a parsed request path.
type ref struct {
	kind kind
	// Cluster and Proc are set for every kind but kindRoot and
	// kindOutputs.
	cluster, proc int
	// name is the sandbox-relative path, for kindJobFile.
	name string
}

// parsePath reads a request path.
//
// The server hands over an absolute, cleaned path, but the components
// are checked anyway: this turns client input into a job id and a
// sandbox name, and a name that is not what it says must be refused
// rather than repaired.
func parsePath(p string) (ref, error) {
	parts := splitPath(p)
	switch len(parts) {
	case 0:
		return ref{kind: kindRoot}, nil
	}
	if parts[0] != outputsDir {
		return ref{}, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}
	if len(parts) == 1 {
		return ref{kind: kindOutputs}, nil
	}

	// parts[1] names a job, either as a directory or as an archive.
	id, isTar := strings.CutSuffix(parts[1], tarSuffix)
	cluster, proc, ok := parseJobID(id)
	if !ok {
		return ref{}, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
	}
	r := ref{cluster: cluster, proc: proc}

	if isTar {
		if len(parts) != 2 {
			// Nothing lives under the archive.
			return ref{}, fmt.Errorf("%q: %w", p, fs.ErrNotExist)
		}
		r.kind = kindJobTar
		return r, nil
	}
	if len(parts) == 2 {
		r.kind = kindJobDir
		return r, nil
	}

	r.kind = kindJobFile
	r.name = path.Join(parts[2:]...)
	return r, nil
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
