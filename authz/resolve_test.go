package authz

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// stubResolver stands in for the network: it records the context each lookup
// is handed and blocks until that context ends, which is what a resolver that
// answers slowly rather than not at all looks like from here.
type stubResolver struct {
	mu          sync.Mutex
	hadDeadline bool
	calls       int
}

func (s *stubResolver) note(ctx context.Context) ([]string, error) {
	s.mu.Lock()
	s.calls++
	_, ok := ctx.Deadline()
	s.hadDeadline = ok
	s.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (s *stubResolver) LookupHost(ctx context.Context, _ string) ([]string, error) {
	return s.note(ctx)
}

func (s *stubResolver) LookupAddr(ctx context.Context, _ string) ([]string, error) {
	return s.note(ctx)
}

// A lookup on the authorization path has to come back.
//
// Verify runs per command, on an address the peer chose, and resolves it
// through a resolver the peer may be able to influence. On context.Background()
// a resolver that accepts the query and then says nothing holds the command
// goroutine for as long as it cares to -- there is no deadline to expire and
// no cancellation to act on, because the Authorizer signature carries no
// context from the connection.
func TestReverseLookupHasADeadline(t *testing.T) {
	stub := &stubResolver{}
	swapResolver(t, stub, 50*time.Millisecond)

	done := make(chan []string, 1)
	go func() { done <- defaultReverseResolve(net.ParseIP("192.0.2.7")) }()

	select {
	case names := <-done:
		if names != nil {
			t.Errorf("a lookup that never answered returned %v", names)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the reverse lookup never returned; it has no deadline")
	}
	if !stub.hadDeadline {
		t.Error("the resolver was handed a context with no deadline")
	}
}

// The same for the forward lookups that expand hostname entries. Those run at
// Init rather than per command, so they get a longer budget -- but a budget.
func TestForwardLookupHasADeadline(t *testing.T) {
	stub := &stubResolver{}
	swapResolver(t, stub, 50*time.Millisecond)

	done := make(chan []string, 1)
	go func() { done <- defaultForwardResolve("cm.example.org") }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the forward lookup never returned; it has no deadline")
	}
	if !stub.hadDeadline {
		t.Error("the resolver was handed a context with no deadline")
	}
}

// swapResolver installs stub as the package resolver and shortens both
// budgets for the duration of the test.
func swapResolver(t *testing.T, stub nameLookup, timeout time.Duration) {
	t.Helper()
	oldR, oldRev, oldFwd := authzResolver, reverseLookupTimeout, forwardLookupTimeout
	authzResolver, reverseLookupTimeout, forwardLookupTimeout = stub, timeout, timeout
	t.Cleanup(func() {
		authzResolver, reverseLookupTimeout, forwardLookupTimeout = oldR, oldRev, oldFwd
	})
}

// One authorization decision, one lookup.
//
// Verify recurses into the permissions that imply the one asked about, and
// every node of that walk used to resolve the peer again: a READ that matches
// nothing walked the whole hierarchy and paid a PTR lookup at each node, so
// one command cost several serial network round trips.
func TestOneDecisionResolvesOnce(t *testing.T) {
	// A site that names hosts rather than networks, at every level: each
	// node of the walk then has a table to consult, so each one reaches
	// the hostname leg. A READ that matches nothing walks the whole
	// hierarchy, which was nine serial lookups for one command.
	cfg := mapConfig{}
	for _, k := range []string{"READ", "WRITE", "DAEMON", "ADMINISTRATOR", "NEGOTIATOR",
		"CONFIG", "ADVERTISE_STARTD", "ADVERTISE_SCHEDD", "ADVERTISE_MASTER", "DEFAULT"} {
		cfg["ALLOW_"+k] = "trusted.example.org"
	}

	var calls int
	p := newTestPolicy(t, cfg, nil, nil)
	p.SetDNSCacheTTL(0) // the memo, not the cache, is what this tests
	p.reverseResolve = func(net.IP) []string {
		calls++
		return []string{"peer.example.org"}
	}

	if p.Verify(PermRead, ip("192.0.2.7"), "someone@example.org") {
		t.Fatal("precondition: the peer matched a rule, so the walk stopped early")
	}
	if calls != 1 {
		t.Errorf("one decision made %d reverse lookups, want 1", calls)
	}
}

// A peer that opens a connection per command pays one lookup, not one per
// command, for as long as the cached answer is fresh.
func TestReverseLookupsAreCachedForTheirTTL(t *testing.T) {
	var calls int
	p := newTestPolicy(t, mapConfig{}, nil, nil)
	p.reverseResolve = func(net.IP) []string {
		calls++
		return []string{"peer.example.org"}
	}
	now := time.Now()
	p.dns.now = func() time.Time { return now }

	peer := ip("192.0.2.7")
	for i := 0; i < 5; i++ {
		p.Verify(PermRead, peer, "someone@example.org")
	}
	if calls != 1 {
		t.Errorf("five decisions made %d lookups, want 1", calls)
	}

	// A different peer is a different answer, so it is a different lookup.
	p.Verify(PermRead, ip("192.0.2.8"), "someone@example.org")
	if calls != 2 {
		t.Errorf("a second peer made %d lookups in total, want 2", calls)
	}

	// Past the TTL the name is looked up again: the cache absorbs a burst,
	// it does not pin a PTR record for the life of the process.
	now = now.Add(DefaultDNSCacheTTL + time.Second)
	p.Verify(PermRead, peer, "someone@example.org")
	if calls != 3 {
		t.Errorf("after the TTL expired there were %d lookups, want 3", calls)
	}
}

// The key space belongs to whoever is connecting, so it has to be bounded.
func TestTheNameCacheIsBounded(t *testing.T) {
	c := newPTRCache(time.Hour)
	resolve := func(net.IP) []string { return []string{"name"} }
	for i := 0; i < dnsCacheMaxEntries+10; i++ {
		c.lookup(net.IPv4(10, byte((i>>16)&0xff), byte((i>>8)&0xff), byte(i&0xff)), resolve)
	}
	c.mu.Lock()
	n := len(c.entries)
	c.mu.Unlock()
	if n > dnsCacheMaxEntries {
		t.Errorf("the cache holds %d entries, past its cap of %d", n, dnsCacheMaxEntries)
	}
}

// A disabled cache resolves every time, which is what a pool whose PTR
// records change and must take effect at once asks for.
func TestADisabledCacheResolvesEveryTime(t *testing.T) {
	var calls int
	p := newTestPolicy(t, mapConfig{}, nil, nil)
	p.SetDNSCacheTTL(0)
	p.reverseResolve = func(net.IP) []string {
		calls++
		return nil
	}
	peer := ip("192.0.2.7")
	p.Verify(PermRead, peer, "someone@example.org")
	p.Verify(PermRead, peer, "someone@example.org")
	if calls != 2 {
		t.Errorf("with the cache off there were %d lookups, want 2", calls)
	}
}
