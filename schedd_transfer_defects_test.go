package htcondor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/PelicanPlatform/classad/classad"
	"github.com/htcondor/cedar/commands"
	"github.com/htcondor/cedar/message"
	"github.com/htcondor/cedar/security"
	cedarserver "github.com/htcondor/cedar/server"
	"github.com/htcondor/cedar/stream"
)

// --- the tar writer's Close error ---

// errWriter fails every write, which is what an http.ResponseWriter on a
// dropped connection and an io.PipeWriter whose reader has gone away both
// do.
type errWriter struct{ err error }

func (w errWriter) Write([]byte) (int, error) { return 0, w.err }

// newEmptySandboxDaemon stands up a schedd that answers
// TRANSFER_DATA_WITH_PERMS with "no jobs matched". The sandbox download
// then writes nothing but the archive's end-of-archive marker, which is
// the one write whose failure used to be discarded.
func newEmptySandboxDaemon(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // test-only loopback listener
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := cedarserver.New(&security.SecurityConfig{
		AuthMethods:    []security.AuthMethod{security.AuthFS},
		Authentication: security.SecurityRequired,
		CryptoMethods:  []security.CryptoMethod{security.CryptoAES},
		Encryption:     security.SecurityOptional,
		Integrity:      security.SecurityOptional,
		SessionCache:   security.NewSessionCache(),
	})
	srv.Handle(int(commands.TRANSFER_DATA_WITH_PERMS), func(ctx context.Context, c *cedarserver.Conn) error {
		in := message.NewMessageFromStream(c.Stream)
		if _, err := in.GetString(ctx); err != nil { // version
			return err
		}
		if _, err := in.GetString(ctx); err != nil { // constraint
			return err
		}
		out := message.NewMessageForStream(c.Stream)
		if err := out.PutInt32(ctx, 0); err != nil {
			return err
		}
		return out.FinishMessage(ctx)
	}, "READ")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = srv.Serve(ctx, ln) }()
	t.Cleanup(func() { cancel(); _ = ln.Close() })
	return fmt.Sprintf("<%s>", ln.Addr().String())
}

// A sandbox download that could not be written must say so.
//
// Everything the caller receives goes through the tar writer, and the
// writer's Close is where the last entry's padding and the end-of-archive
// marker are written. Its error went to a local that nothing read, so a
// download that failed on its last kilobyte was reported as a download
// that succeeded -- and for these callers that log line is the only
// record that anything went wrong.
func TestASandboxDownloadReportsAFailedWrite(t *testing.T) {
	addr := newEmptySandboxDaemon(t)
	s := NewSchedd("fake", addr).WithConfig(mustConfig(t, fsOnlyConfig))

	want := errors.New("the reader went away")
	err := <-s.ReceiveJobSandbox(daemonContext(t), "true", errWriter{err: want})
	if err == nil {
		t.Fatal("a download whose every write failed was reported as a success")
	}
	if !strings.Contains(err.Error(), want.Error()) {
		t.Errorf("the failure was reported as %v, which does not name the write error", err)
	}
}

// --- the empty-spool goroutine ---

// An empty spool that cannot reach the schedd must not leave anything
// behind.
//
// The empty archive used to be written by a goroutine into an io.Pipe, and
// SpoolJobFilesFromTar reads that pipe only after it has built a security
// config, connected, authenticated and sent the PROC_ID list. Every failure
// before the first read left the writer parked on the pipe for the life of
// the process -- one goroutine, one pipe and one tar writer per failed
// submission, in a server that submits all day.
func TestAFailedEmptySpoolLeavesNoGoroutine(t *testing.T) {
	// An address nothing is listening on: the connect fails long before
	// the tar would be read.
	ln, err := net.Listen("tcp", "127.0.0.1:0") //nolint:noctx // test-only loopback listener
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	s := NewSchedd("fake", fmt.Sprintf("<%s>", addr)).WithConfig(mustConfig(t, fsOnlyConfig))
	ad := classad.New()
	_ = ad.Set("ClusterId", int64(1))
	_ = ad.Set("ProcId", int64(0))

	before := goroutinesAtRest()
	for i := 0; i < 20; i++ {
		if err := s.releaseJobsWithEmptySpool(daemonContext(t), []*classad.ClassAd{ad}); err == nil {
			t.Fatal("the spool succeeded against an address nothing is listening on")
		}
	}
	if after := goroutinesAtRest(); after > before+5 {
		t.Errorf("20 failed empty spools left %d goroutines behind (%d -> %d)",
			after-before, before, after)
	}
}

// goroutinesAtRest counts goroutines once the count has stopped moving, so
// a connection teardown still in flight is not read as a leak.
func goroutinesAtRest() int {
	last := runtime.NumGoroutine()
	for i := 0; i < 50; i++ {
		time.Sleep(20 * time.Millisecond)
		runtime.GC()
		n := runtime.NumGoroutine()
		if n == last {
			return n
		}
		last = n
	}
	return last
}

// --- the order of a multi-job spool's segments ---

// spoolToAckingPeer runs sendJobFilesFromTar in multi-job mode against a
// peer that acknowledges each segment, and returns the printable shape of
// what went out. The peer answers rather than only reading because a
// segment is closed with a CommandFinished that waits for an ack, so a
// read-only peer sees only the first segment.
func spoolToAckingPeer(t *testing.T, jobIDs []procID, allow map[procID][]string, tarBytes []byte) (string, error) {
	t.Helper()
	c1, c2 := net.Pipe()
	_ = c1.SetDeadline(time.Now().Add(5 * time.Second))
	_ = c2.SetDeadline(time.Now().Add(5 * time.Second))

	var (
		mu   sync.Mutex
		sent bytes.Buffer
		wg   sync.WaitGroup
	)
	wg.Add(2)
	peer := stream.NewStream(c2)
	go func() {
		defer wg.Done()
		// One ack per segment the sender can close, written up front:
		// CEDAR frames are self-delimiting, so each is read as its own
		// segment is finished.
		for i := 0; i < len(jobIDs)+2; i++ {
			ack := classad.New()
			_ = ack.Set("Result", int64(0))
			_ = ack.Set("TransferStats", classad.New())
			m := message.NewMessageForStream(peer)
			if m.PutClassAd(context.Background(), ack) != nil {
				return
			}
			if m.FinishMessage(context.Background()) != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, err := c2.Read(buf)
			mu.Lock()
			sent.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()

	info := make(map[procID]*jobInfo, len(jobIDs))
	for _, id := range jobIDs {
		set := make(map[string]string)
		for _, n := range allow[id] {
			set[n] = n
		}
		info[id] = &jobInfo{inputFiles: set, jobID: id}
	}
	err := (&Schedd{}).sendJobFilesFromTar(context.Background(), stream.NewStream(c1),
		bytes.NewReader(tarBytes), info, jobIDs, false)
	_ = c1.Close()
	_ = c2.Close()
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	return strings.Map(func(r rune) rune {
		if r >= 32 && r < 127 {
			return r
		}
		return '.'
	}, sent.String()), err
}

// The segment for a job with no files in the tar has to be sent in its
// place in the job list, not appended after the others.
//
// The schedd pairs the i'th transfer segment with the i'th PROC_ID it was
// sent. A job whose files the tar does not carry used to have its empty
// segment appended at the end, so with two jobs and files for the second
// one only, the schedd read the second job's files into the first job's
// spool and then released both. The wronged job fails at execute time
// with a file it never asked for, and nothing reports a failed transfer.
func TestAMultiJobSpoolSendsSegmentsInJobOrder(t *testing.T) {
	first := procID{cluster: 1, proc: 0}
	second := procID{cluster: 1, proc: 1}

	wire, _ := spoolToAckingPeer(t,
		[]procID{first, second},
		map[procID][]string{second: {"in.txt"}},
		buildTar(t, []tarEntry{{name: "1.1/in.txt", data: "hello"}}))

	empty := strings.Index(wire, "SandboxSize = 0")
	full := strings.Index(wire, "SandboxSize = 1048576")
	if empty < 0 {
		t.Fatalf("the job with no files in the tar never got a segment: %s", wire)
	}
	if full < 0 {
		t.Fatalf("the job with files never got a segment: %s", wire)
	}
	if empty > full {
		t.Errorf("the segments left in tar order, so the second job's files go to the first job's spool: %s", wire)
	}
}

// A tar that comes back to a job whose segment is already closed cannot be
// served: this is a stream, there is nothing to reorder, and sending the
// files now would put them in whichever segment is open. Refuse it rather
// than deliver it into the wrong job's spool.
func TestAMultiJobSpoolRefusesAnOutOfOrderTar(t *testing.T) {
	first := procID{cluster: 1, proc: 0}
	second := procID{cluster: 1, proc: 1}

	// Names outside the allow-set, so the entries open their segments
	// and are then skipped: the order is what is under test, not the
	// file transfer.
	_, err := spoolToAckingPeer(t,
		[]procID{first, second},
		nil,
		buildTar(t, []tarEntry{
			{name: "1.1/skipped", data: "b"},
			{name: "1.0/skipped", data: "a"},
		}))
	if !errors.Is(err, ErrSpoolTarOutOfOrder) {
		t.Errorf("err = %v, want ErrSpoolTarOutOfOrder", err)
	}
}

// A tar in job order is served, and nothing above refuses it.
func TestAMultiJobSpoolInJobOrderIsServed(t *testing.T) {
	first := procID{cluster: 1, proc: 0}
	second := procID{cluster: 1, proc: 1}

	_, err := spoolToAckingPeer(t,
		[]procID{first, second},
		nil,
		buildTar(t, []tarEntry{
			{name: "1.0/skipped", data: "a"},
			{name: "1.1/skipped", data: "b"},
		}))
	if err != nil {
		t.Errorf("a tar in job order was refused: %v", err)
	}
}
