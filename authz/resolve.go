package authz

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
)

// Name resolution on the authorization path.
//
// Verify runs per command, on an address the peer chose, and a reverse lookup
// is a blocking network call to a resolver the peer may be able to influence.
// Left unbounded it holds the command goroutine for as long as the resolver
// takes to give up -- which, with a resolver that answers slowly rather than
// not at all, is unbounded. These deadlines cap that hold.
//
// They are variables rather than constants so a test can lower them; nothing
// in production writes to them.
var (
	// reverseLookupTimeout bounds the PTR lookup Verify makes.
	reverseLookupTimeout = 5 * time.Second
	// forwardLookupTimeout bounds the A/AAAA lookups that expand hostname
	// entries. That runs at Init, not per command, so it can afford more.
	forwardLookupTimeout = 20 * time.Second
	// authzResolver is the resolver both use.
	authzResolver nameLookup = net.DefaultResolver
)

// nameLookup is the slice of *net.Resolver this package needs. An interface
// so a test can stand in for the network and watch what context the lookup
// is handed.
type nameLookup interface {
	LookupHost(ctx context.Context, host string) ([]string, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

func defaultForwardResolve(host string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), forwardLookupTimeout)
	defer cancel()
	ips, err := authzResolver.LookupHost(ctx, host)
	if err != nil {
		return nil
	}
	return ips
}

func defaultReverseResolve(ip net.IP) []string {
	if ip == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), reverseLookupTimeout)
	defer cancel()
	names, err := authzResolver.LookupAddr(ctx, ip.String())
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, strings.TrimSuffix(n, "."))
	}
	return out
}

// DefaultDNSCacheTTL is how long a peer's resolved names are reused.
//
// The cache exists to collapse the repeats of a connection burst, not to hold
// DNS for a working day: a peer that opens one connection per command pays one
// lookup rather than one per command. It is far shorter than the eight hours
// C++ leaves between DNS_CACHE_REFRESH passes over comparable data, so a
// changed PTR record takes effect sooner here than there.
const DefaultDNSCacheTTL = 30 * time.Second

// dnsCacheMaxEntries caps the cache. The keys are peer addresses, so the key
// space belongs to whoever is connecting; without a cap a peer rotating
// through a /64 would grow the map for the life of the process.
const dnsCacheMaxEntries = 1024

// ptrCache is a small TTL'd cache of reverse lookups, keyed by peer address.
type ptrCache struct {
	mu      sync.Mutex
	entries map[string]ptrEntry
	ttl     time.Duration
	// now is time.Now, replaced in tests.
	now func() time.Time
}

type ptrEntry struct {
	names []string
	at    time.Time
}

func newPTRCache(ttl time.Duration) *ptrCache {
	return &ptrCache{
		entries: make(map[string]ptrEntry),
		ttl:     ttl,
		now:     time.Now,
	}
}

// lookup returns the cached names for ip, calling resolve on a miss or an
// expired entry. A ttl of zero or less disables the cache entirely.
func (c *ptrCache) lookup(ip net.IP, resolve func(net.IP) []string) []string {
	if c == nil || c.ttl <= 0 || ip == nil {
		return resolve(ip)
	}
	key := ip.String()

	c.mu.Lock()
	e, ok := c.entries[key]
	fresh := ok && c.now().Sub(e.at) < c.ttl
	c.mu.Unlock()
	if fresh {
		return e.names
	}

	// Resolve outside the lock: it is a network call, and holding the
	// mutex across it would serialize every authorization in the daemon
	// behind one slow peer. Two peers racing the same address both
	// resolve, which costs one extra lookup and no correctness.
	names := resolve(ip)

	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= dnsCacheMaxEntries {
		c.evictLocked()
	}
	c.entries[key] = ptrEntry{names: names, at: c.now()}
	return names
}

// evictLocked drops expired entries, and if that was not enough, everything.
// Clearing wholesale rather than tracking use order: the cache is a burst
// absorber, so the cost of a cleared cache is one lookup per live peer, and
// a peer trying to fill it gets nothing from doing so.
func (c *ptrCache) evictLocked() {
	now := c.now()
	for k, e := range c.entries {
		if now.Sub(e.at) >= c.ttl {
			delete(c.entries, k)
		}
	}
	if len(c.entries) >= dnsCacheMaxEntries {
		clear(c.entries)
	}
}

// peerNames resolves a peer's hostnames at most once per authorization
// decision.
//
// Verify recurses into the permissions that imply the one asked about -- up to
// nine nodes for a READ that matches nothing -- and each node used to repeat
// the lookup. Resolving once per decision also keeps one decision from seeing
// two different answers across a cache expiry.
type peerNames struct {
	resolve func() []string
	names   []string
	done    bool
}

func (n *peerNames) get() []string {
	if !n.done {
		n.names = n.resolve()
		n.done = true
	}
	return n.names
}
