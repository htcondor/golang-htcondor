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

package sandboxfs

import (
	"container/list"
	"context"
	"fmt"
	"io"
	"sync"

	"golang.org/x/sync/singleflight"
)

// Fetcher streams one job's sandbox.
type Fetcher interface {
	// FetchSandbox writes job cluster.proc's sandbox to w as a tar.
	//
	// onPreamble, when non-nil, is called with the size the sender
	// advertised before any file arrives; an error from it aborts the
	// transfer and FetchSandbox returns it. known is false when the
	// sender advertised nothing, and size is then 0 rather than meaning
	// an empty sandbox.
	FetchSandbox(ctx context.Context, cluster, proc int, w io.Writer,
		onPreamble func(size int64, known bool) error) error
}

// Defaults for Config.
const (
	// DefaultMaxSandboxBytes is the largest sandbox served as files.
	//
	// The limit protects the layout pass more than any disk: building a
	// layout drains the whole sandbox at full bandwidth to keep a few
	// kilobytes of names and sizes, and that is the cost a ceiling
	// bounds. Refusing is cheap because the size arrives in the preamble
	// before any byte does.
	DefaultMaxSandboxBytes = int64(8) << 30

	// DefaultMaxEntriesPerJob bounds one sandbox's name count.
	DefaultMaxEntriesPerJob = 20000

	// DefaultMaxCachedEntries bounds the layout cache across all jobs.
	// At roughly a hundred bytes an entry this is single-digit megabytes.
	DefaultMaxCachedEntries = 200000
)

// Config tunes a Store.
type Config struct {
	// MaxSandboxBytes refuses a job whose advertised total exceeds it.
	// Negative means no limit; zero takes DefaultMaxSandboxBytes.
	MaxSandboxBytes int64

	// MaxEntriesPerJob refuses a sandbox with more names than this.
	// Negative means no limit; zero takes DefaultMaxEntriesPerJob.
	MaxEntriesPerJob int

	// MaxCachedEntries is the layout cache's budget, counted in entries
	// across every cached job. Negative means no limit; zero takes
	// DefaultMaxCachedEntries.
	//
	// A target rather than a guarantee: a single job's layout is cached
	// even when it alone exceeds the budget, because evicting everything
	// and still being over serves nobody.
	MaxCachedEntries int

	// ServeUnadvertisedSize serves a sandbox whose sender advertised no
	// size at all. Off by default: with no size there is nothing for
	// MaxSandboxBytes to judge, and draining an unbounded sandbox blind
	// is the cost the limit exists to avoid.
	ServeUnadvertisedSize bool

	// CacheDir is where the byte cache keeps files. Required when
	// CacheBytes is positive; its contents are discarded at startup.
	CacheDir string

	// CacheBytes is the byte cache's budget. Zero or less disables it, so
	// every read streams. See dataCache: the count is a target, not a
	// guarantee.
	CacheBytes int64

	// Logf, if set, receives debug logging.
	Logf func(format string, args ...any)
}

func (c Config) maxSandboxBytes() int64 {
	switch {
	case c.MaxSandboxBytes < 0:
		return 0
	case c.MaxSandboxBytes == 0:
		return DefaultMaxSandboxBytes
	}
	return c.MaxSandboxBytes
}

func (c Config) maxEntriesPerJob() int {
	switch {
	case c.MaxEntriesPerJob < 0:
		return 0
	case c.MaxEntriesPerJob == 0:
		return DefaultMaxEntriesPerJob
	}
	return c.MaxEntriesPerJob
}

func (c Config) maxCachedEntries() int {
	switch {
	case c.MaxCachedEntries < 0:
		return 0
	case c.MaxCachedEntries == 0:
		return DefaultMaxCachedEntries
	}
	return c.MaxCachedEntries
}

func (c Config) logf(format string, args ...any) {
	if c.Logf != nil {
		c.Logf(format, args...)
	}
}

// TooLargeError reports a sandbox refused for its size. It carries both
// numbers because the only useful thing to tell somebody whose download
// was refused is how far over they are.
type TooLargeError struct {
	Cluster, Proc int
	Size, Limit   int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("sandboxfs: job %d.%d has a %d byte sandbox, over the %d byte limit",
		e.Cluster, e.Proc, e.Size, e.Limit)
}

// SizeUnknownError reports a sandbox whose sender advertised no size,
// which leaves a size limit nothing to judge.
type SizeUnknownError struct {
	Cluster, Proc int
}

func (e *SizeUnknownError) Error() string {
	return fmt.Sprintf("sandboxfs: job %d.%d advertised no sandbox size", e.Cluster, e.Proc)
}

// Store caches job sandbox layouts.
//
// Safe for concurrent use. Two callers asking for the same layout at
// once share one pass over the sandbox, which is the one duplicate worth
// preventing here: it is a full-bandwidth drain either way.
type Store struct {
	fetcher Fetcher
	cfg     Config

	data *dataCache

	// group shares a layout pass; fill shares a cache-filling pass. Kept
	// apart because one is cheap to repeat and the other is not: two
	// layout passes waste a drain whose result is a few kilobytes, while
	// a second fill would re-stage files the first already published.
	group singleflight.Group
	fill  singleflight.Group

	mu      sync.Mutex
	index   map[jobKey]*list.Element
	lru     *list.List // front = most recently used; values are *cacheEntry
	entries int
}

type jobKey struct{ cluster, proc int }

func (k jobKey) String() string { return fmt.Sprintf("%d.%d", k.cluster, k.proc) }

type cacheEntry struct {
	key    jobKey
	layout *Layout
}

// New returns a Store that reads sandboxes through f.
//
// Returns an error only for the byte cache's directory, which has to be
// usable before anything is served; with CacheBytes at zero there is no
// directory and no error.
func New(f Fetcher, cfg Config) (*Store, error) {
	data, err := newDataCache(cfg.CacheDir, cfg.CacheBytes, cfg.Logf)
	if err != nil {
		return nil, err
	}
	return &Store{
		fetcher: f,
		cfg:     cfg,
		data:    data,
		index:   map[jobKey]*list.Element{},
		lru:     list.New(),
	}, nil
}

// Layout returns job cluster.proc's sandbox layout, building it from one
// pass over the sandbox the first time and from the cache afterwards.
//
// A finished job's sandbox does not change, so a cached layout needs no
// expiry; Forget drops one when a caller knows better.
func (s *Store) Layout(ctx context.Context, cluster, proc int) (*Layout, error) {
	key := jobKey{cluster, proc}
	if l, ok := s.cached(key); ok {
		return l, nil
	}

	// singleflight shares one pass; Do returns the same result to every
	// waiter, and the shared flag is not interesting here because the
	// layout is immutable.
	v, err, _ := s.group.Do(key.String(), func() (any, error) {
		// Another caller may have finished while this one waited.
		if l, ok := s.cached(key); ok {
			return l, nil
		}
		l, err := s.build(ctx, key)
		if err != nil {
			return nil, err
		}
		s.store(key, l)
		return l, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*Layout), nil
}

// Forget drops everything cached for a job: its layout and its bytes.
func (s *Store) Forget(cluster, proc int) {
	key := jobKey{cluster, proc}
	s.mu.Lock()
	s.removeLocked(key)
	s.mu.Unlock()
	s.data.EvictJob(key)
}

// CachedBytes is what the byte cache believes it holds.
func (s *Store) CachedBytes() int64 { return s.data.Bytes() }

// CachedEntries reports how many entries the layout cache holds, for
// tests and for a metric.
func (s *Store) CachedEntries() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.entries
}

func (s *Store) cached(key jobKey) (*Layout, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	el, ok := s.index[key]
	if !ok {
		return nil, false
	}
	s.lru.MoveToFront(el)
	return el.Value.(*cacheEntry).layout, true
}

func (s *Store) store(key jobKey, l *Layout) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.removeLocked(key)
	s.index[key] = s.lru.PushFront(&cacheEntry{key: key, layout: l})
	s.entries += len(l.Entries)

	// Evict while over budget, but never to empty: one layout larger than
	// the whole budget stays, because dropping everything else and still
	// being over helps nobody.
	max := s.cfg.maxCachedEntries()
	for max > 0 && s.entries > max && s.lru.Len() > 1 {
		back := s.lru.Back()
		if back == nil {
			break
		}
		s.removeLocked(back.Value.(*cacheEntry).key)
	}
}

func (s *Store) removeLocked(key jobKey) {
	el, ok := s.index[key]
	if !ok {
		return
	}
	s.entries -= len(el.Value.(*cacheEntry).layout.Entries)
	s.lru.Remove(el)
	delete(s.index, key)
}

// build makes one pass over the sandbox, recording names and sizes and
// keeping no bytes.
func (s *Store) build(ctx context.Context, key jobKey) (*Layout, error) {
	var (
		advertised int64
		known      bool
	)
	enforce := s.gate(key)
	gate := func(size int64, sizeKnown bool) error {
		advertised, known = size, sizeKnown
		return enforce(size, sizeKnown)
	}

	pr, pw := io.Pipe()
	go func() {
		err := s.fetcher.FetchSandbox(ctx, key.cluster, key.proc, pw, gate)
		// CloseWithError(nil) is a plain Close, so the reader sees EOF on
		// success and the refusal or transport error otherwise.
		_ = pw.CloseWithError(err)
	}()

	l, err := recordLayout(pr, s.cfg.maxEntriesPerJob())
	// Unblock the fetcher when the record side gave up first, e.g. on the
	// entry limit; without this it parks on a write nobody will read.
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, err
	}
	_ = pr.Close()

	l.Advertised, l.AdvertisedKnown = advertised, known
	s.cfg.logf("sandboxfs: job %s layout: %d entries, %d bytes (advertised %d, known=%v)",
		key, len(l.Entries), l.Bytes(), advertised, known)
	return l, nil
}
