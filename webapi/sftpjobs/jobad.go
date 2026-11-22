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

package sftpjobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/PelicanPlatform/classad/classad"
)

// job.ad is the job itself, beside its output.
//
// Output on its own answers "what did it produce" and nothing else. How
// much memory it used, what it exited with, when it ran, where it ran,
// what it was told to run, which template or archive it came from --
// all of that is in the job's ad, and over SFTP there was no way to
// read it. The alternative is `condor_q -l` from a shell, which is the
// thing this service exists to avoid needing.
//
// Rendered as old-ClassAd text, one `Attr = value` line per attribute,
// sorted: the format HTCondor's own starter writes into the execute
// directory as .job.ad, and the format `condor_q -l` prints. Somebody
// who has seen either of those can read this one.

// JobAdReader fetches one job's ad.
//
// Optional, and separate from JobLister on purpose: a view with no
// reader simply has no job.ad, in the same way that a view with no
// lister still addresses a job by id. Whoever wires this up decides
// where an ad comes from -- the live queue, the mirror, history -- and
// is responsible for answering only for jobs the account may read.
type JobAdReader interface {
	JobAd(ctx context.Context, cluster, proc int) (*classad.ClassAd, error)
}

// jobAdText renders a job's ad, from the per-channel cache when it has
// been asked for already.
//
// Cached because a client asks three times for one look: a stat before
// the open, the listing that showed it, and the read itself. The cache
// lives as long as the channel, which is also as long as the answer can
// be trusted -- the ad of a job that has not finished changes, and a
// client that reconnects gets a fresh one.
func (f *FS) jobAdText(ctx context.Context, r ref) (string, error) {
	if f.cfg.JobAds == nil {
		return "", fmt.Errorf("this access point does not serve job ads")
	}
	key := [2]int{r.cluster, r.proc}

	f.mu.Lock()
	text, ok := f.adCache[key]
	f.mu.Unlock()
	if ok {
		return text, nil
	}

	ad, err := f.cfg.JobAds.JobAd(ctx, r.cluster, r.proc)
	if err != nil {
		return "", err
	}
	if ad == nil {
		return "", fmt.Errorf("no ad for job %s", jobDirName(r.cluster, r.proc))
	}
	text = renderJobAd(ad)

	f.mu.Lock()
	if f.adCache == nil {
		f.adCache = map[[2]int]string{}
	}
	f.adCache[key] = text
	f.mu.Unlock()
	return text, nil
}

// renderJobAd is the ad as a file.
//
// MarshalOld rather than a hand-written loop: it is the library's own
// old-ClassAd writer, and the formats a hand-written one gets wrong are
// not obvious ones -- a real is %.16G with a forced ".0", a string
// needs the old escaping rules. One newline at the end, because a file
// that does not end in one makes `cat` run its last line into the
// shell prompt.
func renderJobAd(ad *classad.ClassAd) string {
	text := ad.MarshalOld()
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text
}
