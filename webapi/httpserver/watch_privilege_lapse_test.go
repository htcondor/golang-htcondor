package httpserver

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/htcondor/golang-htcondor/webapi/poolpolicy"
)

// armedWatchCaller brings up a server under the given pool policy and returns
// it with a request carrying a browser session armed as a global superuser,
// plus the session id so a test can take the arm away.
func armedWatchCaller(t *testing.T, p *poolpolicy.Policy) (*Server, *http.Request, string) {
	t.Helper()
	server := newPoolPolicyServer(t, p)
	h := server.Handler
	h.initSuperuserMode(HandlerConfig{SuperuserGroup: "condor-su"}, h.logger)
	if !h.superuserModeAvailable() {
		t.Fatal("superuser mode did not come up")
	}
	sid, _, err := h.sessionStore.Create("alice", []string{"condor-su"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	h.superuserArmed.Arm(sid, armedSession{identity: "alice@test.htcondor.org"})

	r := httptest.NewRequestWithContext(context.Background(), http.MethodGet,
		"/api/v1/collector/watch?type=StartdAd", nil)
	r.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sid}) //nolint:gosec // test cookie
	return server, r, sid
}

// The predicate a running collector watch is re-checked against has to be the
// one the open applied, and it has to follow the caller's privilege rather
// than the boolean the open produced.
//
// privileged decides both the open-time gates and FilterClassAd on every
// event, so a stream opened while the caller could read the pool keeps
// emitting unredacted StartdAds -- RemoteOwner, RemoteUser, RemoteProject,
// GlobalJobId -- after that stops being true. Armed superuser mode self-
// disarms after thirty minutes, and an explicit disarm, a logout, an expired
// session or a withdrawn group membership take it away sooner.
func TestCollectorWatchRefusalFollowsTheArm(t *testing.T) {
	for _, tc := range []struct {
		mode       poolpolicy.Mode
		refusesNow bool
	}{
		// Restricted narrows rather than refuses: the stream stays open
		// and FilterClassAd starts redacting from the next event.
		{mode: poolpolicy.ModeRestricted, refusesNow: false},
		// Admin refuses outright, so the stream has to end.
		{mode: poolpolicy.ModeAdmin, refusesNow: true},
	} {
		t.Run(tc.mode.String(), func(t *testing.T) {
			server, r, sid := armedWatchCaller(t, poolpolicy.New(tc.mode, nil))
			h := server.Handler
			ctx := context.Background()

			if !h.seesAllJobs(ctx, r, jobScopeRead) {
				t.Fatal("precondition: the armed caller does not see all jobs")
			}
			privileged, err := h.collectorWatchRecheck(ctx, r, "StartdAd", "")
			if !privileged || err != nil {
				t.Fatalf("the armed caller was refused: privileged=%v err=%v", privileged, err)
			}

			h.superuserArmed.Disarm(sid)

			// What the running stream now carries. Before the
			// re-check it kept the open-time true for the life of
			// the stream.
			privileged, err = h.collectorWatchRecheck(ctx, r, "StartdAd", "")
			if privileged {
				t.Error("the stream still runs privileged after the arm was taken away")
			}
			if tc.refusesNow && err == nil {
				t.Error("a disarmed caller is still served the stream under admin mode")
			}
			if !tc.refusesNow {
				if err != nil {
					t.Errorf("a disarmed caller was disconnected where redaction would do: %v", err)
				}
				// Under restricted mode the narrowing is what
				// protects the ads, so check it narrows.
				if !h.poolQueryPolicy.Restricts(privileged) {
					t.Error("a disarmed caller is not held to the allowlist")
				}
			}
		})
	}
}

// A stream opened for everyone's jobs must end when the caller may no longer
// read them.
//
// The jobs watch and the activity streams bake the owner scope into the
// constraint or the feed subscription at open, so unlike the collector watch
// there is nothing to narrow afterwards -- ending the stream is the
// narrowing. EventSource reconnects and the new request is scoped afresh.
func TestAStreamForEveryOwnerEndsWhenTheArmLapses(t *testing.T) {
	server, r, sid := armedWatchCaller(t, poolpolicy.New(poolpolicy.ModeOpen, nil))
	h := server.Handler
	h.jobWatchRecheck = 20 * time.Millisecond

	ctx := h.watchAllJobsScope(context.Background(), r, true)
	select {
	case <-ctx.Done():
		t.Fatal("the stream ended while the caller was still armed")
	case <-time.After(100 * time.Millisecond):
	}

	h.superuserArmed.Disarm(sid)

	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the stream outlived the privilege that admitted it")
	}
}

// A caller confined to their own jobs has nothing to lose, so the re-check is
// not armed for them and they pay nothing: no goroutine, no ticker, no
// repeated privilege lookup for the life of the stream.
func TestAnOwnScopedStreamIsNotRechecked(t *testing.T) {
	server, r, _ := armedWatchCaller(t, poolpolicy.New(poolpolicy.ModeOpen, nil))
	h := server.Handler
	h.jobWatchRecheck = 20 * time.Millisecond

	base, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx := h.watchAllJobsScope(base, r, false)

	if ctx != base {
		t.Error("an own-scoped stream was given a derived context, so it pays for a re-check it cannot need")
	}
}
