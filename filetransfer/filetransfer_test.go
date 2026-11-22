// Copyright 2025 Morgridge Institute for Research
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

package filetransfer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/bbockelm/cedar/stream"
)

// memSink collects received files in memory.
type memSink struct {
	files map[string][]byte
	modes map[string]int64
	dirs  []string
}

func newMemSink() *memSink {
	return &memSink{files: map[string][]byte{}, modes: map[string]int64{}}
}

func (m *memSink) Mkdir(name string) error {
	m.dirs = append(m.dirs, name)
	return nil
}

type memWriter struct {
	name string
	buf  bytes.Buffer
	sink *memSink
	mode int64
}

func (w *memWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }
func (w *memWriter) Close() error {
	w.sink.files[w.name] = w.buf.Bytes()
	w.sink.modes[w.name] = w.mode
	return nil
}

func (m *memSink) File(name string, mode int64, _ int64) (io.WriteCloser, error) {
	return &memWriter{name: name, sink: m, mode: mode}, nil
}

// TestSendReceiveRoundTrip drives SendStream and ReceiveStream against each
// other over a net.Pipe and asserts the received bytes match, exercising the
// preamble, go-ahead handshake, per-file framing, mkdir and the TransferAck
// exchange.
func TestSendReceiveRoundTrip(t *testing.T) {
	c1, c2 := net.Pipe()
	sender := stream.NewStream(c1)
	receiver := stream.NewStream(c2)

	payloads := map[string][]byte{
		"input.dat": bytes.Repeat([]byte("abc\n"), 100000), // > one buffer
		"small.txt": []byte("hello world"),
		"empty":     {},
	}
	plan := SendPlan{FinalTransfer: true}
	plan.Files = append(plan.Files, FileSpec{WireName: "sub", Dir: true})
	for _, name := range []string{"input.dat", "small.txt", "empty"} {
		data := payloads[name]
		plan.Files = append(plan.Files, FileSpec{
			WireName: name,
			Mode:     0644,
			Size:     int64(len(data)),
			Open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
		})
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendStream(ctx, sender, plan, Options{Logf: t.Logf, ReceiveAck: false})
	}()

	sink := newMemSink()
	res, err := ReceiveStream(ctx, receiver, sink, Options{Logf: t.Logf, ReceiveAck: true})
	if err != nil {
		t.Fatalf("ReceiveStream: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("SendStream: %v", err)
	}

	if len(res.Files) != 3 {
		t.Errorf("received %d files, want 3: %v", len(res.Files), res.Files)
	}
	if len(sink.dirs) != 1 || sink.dirs[0] != "sub" {
		t.Errorf("dirs = %v, want [sub]", sink.dirs)
	}
	for name, want := range payloads {
		if got, ok := sink.files[name]; !ok {
			t.Errorf("missing file %q", name)
		} else if !bytes.Equal(got, want) {
			t.Errorf("file %q: got %d bytes, want %d", name, len(got), len(want))
		}
	}
}

// preambleSink records whether any file or directory arrived, so a test can
// assert the ORDER of the preamble hook against the first file rather than
// only its argument. A hook that fired at the end of the stream would carry
// the right size and still be useless for refusing a transfer.
type preambleSink struct {
	*memSink
	sawFile func()
}

func (p preambleSink) File(name string, mode int64, size int64) (io.WriteCloser, error) {
	p.sawFile()
	return p.memSink.File(name, mode, size)
}

func (p preambleSink) Mkdir(name string) error {
	p.sawFile()
	return p.memSink.Mkdir(name)
}

// onePlan is a one-file SendPlan advertising sandboxSize in the preamble.
func onePlan(sandboxSize int64) SendPlan {
	data := []byte("payload")
	return SendPlan{
		FinalTransfer: true,
		SandboxSize:   sandboxSize,
		Files: []FileSpec{{
			WireName: "f.dat",
			Mode:     0644,
			Size:     int64(len(data)),
			Open:     func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil },
		}},
	}
}

// TestReceivePreambleHookPrecedesFiles asserts OnPreamble is called with the
// advertised sandbox size before the first file reaches the Sink. The ordering
// is the whole point: it is what lets a receiver refuse an oversized sandbox
// without paying for it.
func TestReceivePreambleHookPrecedesFiles(t *testing.T) {
	c1, c2 := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Deliberately not the sum of the files, so a size computed from the
	// per-file headers could not produce it.
	const advertised = int64(123456789)

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendStream(ctx, stream.NewStream(c1), onePlan(advertised), Options{Logf: t.Logf})
	}()

	var (
		hookCalls   int
		gotSize     int64
		gotKnown    bool
		filesAtHook = -1
		files       int
	)
	sink := preambleSink{memSink: newMemSink(), sawFile: func() { files++ }}
	res, err := ReceiveStream(ctx, stream.NewStream(c2), sink, Options{
		Logf:       t.Logf,
		ReceiveAck: true,
		OnPreamble: func(size int64, known bool) error {
			hookCalls++
			gotSize, gotKnown, filesAtHook = size, known, files
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ReceiveStream: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("SendStream: %v", err)
	}

	if hookCalls != 1 {
		t.Errorf("OnPreamble called %d times, want 1", hookCalls)
	}
	if filesAtHook != 0 {
		t.Errorf("%d files had arrived when OnPreamble ran, want 0", filesAtHook)
	}
	if gotSize != advertised || !gotKnown {
		t.Errorf("OnPreamble(%d, %v), want (%d, true)", gotSize, gotKnown, advertised)
	}
	if res.SandboxSize != advertised || !res.SandboxSizeKnown {
		t.Errorf("result size = (%d, %v), want (%d, true)", res.SandboxSize, res.SandboxSizeKnown, advertised)
	}
	if files != 1 {
		t.Errorf("sink saw %d files, want 1", files)
	}
}

// TestReceivePreambleHookRefuses asserts an error from OnPreamble aborts the
// receive and that nothing reached the Sink.
func TestReceivePreambleHookRefuses(t *testing.T) {
	c1, c2 := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendStream(ctx, stream.NewStream(c1), onePlan(1<<40), Options{Logf: t.Logf})
	}()

	refusal := errors.New("sandbox too large")
	files := 0
	sink := preambleSink{memSink: newMemSink(), sawFile: func() { files++ }}
	_, err := ReceiveStream(ctx, stream.NewStream(c2), sink, Options{
		Logf:       t.Logf,
		OnPreamble: func(int64, bool) error { return refusal },
	})
	if !errors.Is(err, refusal) {
		t.Fatalf("ReceiveStream error = %v, want %v", err, refusal)
	}
	if files != 0 {
		t.Errorf("sink saw %d files after a refused preamble, want 0", files)
	}

	// The sender is parked on a write nobody will read; closing both ends
	// is what unblocks it, and its error is not what this test is about.
	_ = c2.Close()
	_ = c1.Close()
	select {
	case <-sendErr:
	case <-time.After(10 * time.Second):
		t.Error("SendStream did not return after the receiver hung up")
	}
}

// TestReceivePreambleHookWithoutXferInfo asserts that a peer sending no
// xfer_info ad reports known=false rather than a zero size that would read as
// an empty sandbox.
func TestReceivePreambleHookWithoutXferInfo(t *testing.T) {
	c1, c2 := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sendErr := make(chan error, 1)
	go func() {
		sendErr <- SendStream(ctx, stream.NewStream(c1), onePlan(999), Options{Logf: t.Logf, NoXferInfo: true})
	}()

	var gotSize int64 = -1
	gotKnown := true
	res, err := ReceiveStream(ctx, stream.NewStream(c2), newMemSink(), Options{
		Logf:       t.Logf,
		ReceiveAck: true,
		NoXferInfo: true,
		OnPreamble: func(size int64, known bool) error {
			gotSize, gotKnown = size, known
			return nil
		},
	})
	if err != nil {
		t.Fatalf("ReceiveStream: %v", err)
	}
	if err := <-sendErr; err != nil {
		t.Fatalf("SendStream: %v", err)
	}
	if gotSize != 0 || gotKnown {
		t.Errorf("OnPreamble(%d, %v), want (0, false)", gotSize, gotKnown)
	}
	if res.SandboxSizeKnown {
		t.Error("SandboxSizeKnown is true although the peer sent no xfer_info")
	}
}
