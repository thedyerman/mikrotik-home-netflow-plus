package enrich

import (
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/publicsuffix"
)

// DNSRecord is one entry of the router's DNS cache.
type DNSRecord struct {
	Type string // A, AAAA, CNAME
	Name string
	Data string
}

type nameEntry struct {
	name  string
	seen  int64
	dirty bool
}

// Names remembers which DNS name each remote address was resolved from. The
// router's cache only holds a record until its TTL runs out, so the mapping is
// accumulated across polls and kept.
type Names struct {
	mu      sync.RWMutex
	byIP    map[netip.Addr]*nameEntry
	max     int
	matched uint64
	lookups uint64
}

// NewNames creates a table holding at most max addresses.
func NewNames(max int) *Names {
	return &Names{byIP: map[netip.Addr]*nameEntry{}, max: max}
}

// Restore loads a mapping saved earlier.
func (n *Names) Restore(ip, name string, seen int64) {
	if a, err := netip.ParseAddr(ip); err == nil {
		n.mu.Lock()
		n.byIP[a] = &nameEntry{name: name, seen: seen}
		n.mu.Unlock()
	}
}

// Observe folds one snapshot of the router's DNS cache into the table.
func (n *Names) Observe(records []DNSRecord, now time.Time) {
	// alias[target] = the name that points at it; walking it upwards from an A
	// record's owner recovers the name the client actually asked for.
	alias := map[string]string{}
	for _, r := range records {
		if r.Type == "CNAME" {
			alias[normName(r.Data)] = normName(r.Name)
		}
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, r := range records {
		if r.Type != "A" && r.Type != "AAAA" {
			continue
		}
		a, err := netip.ParseAddr(r.Data)
		if err != nil {
			continue
		}
		name := normName(r.Name)
		for i := 0; i < 8; i++ {
			up, ok := alias[name]
			if !ok || up == name {
				break
			}
			name = up
		}
		if name == "" {
			continue
		}
		if e := n.byIP[a]; e != nil {
			if e.name != name {
				e.name, e.dirty = name, true
			} else if now.Unix()-e.seen > 3600 {
				e.dirty = true // refresh the stored timestamp now and then
			}
			e.seen = now.Unix()
		} else {
			n.byIP[a] = &nameEntry{name: name, seen: now.Unix(), dirty: true}
		}
	}
	if len(n.byIP) > n.max {
		n.evict()
	}
}

// evict drops the oldest tenth of the table. Caller holds the lock.
func (n *Names) evict() {
	seen := make([]int64, 0, len(n.byIP))
	for _, e := range n.byIP {
		seen = append(seen, e.seen)
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })
	cut := seen[len(seen)/10]
	for a, e := range n.byIP {
		if e.seen <= cut {
			delete(n.byIP, a)
		}
	}
}

// Lookup returns the name an address was resolved from.
func (n *Names) Lookup(a netip.Addr) (string, bool) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lookups++
	if e := n.byIP[a]; e != nil {
		n.matched++
		return e.name, true
	}
	return "", false
}

// Dirty returns the mappings that changed since the last call.
func (n *Names) Dirty() (ips, names []string, seen []int64) {
	n.mu.Lock()
	defer n.mu.Unlock()
	for a, e := range n.byIP {
		if e.dirty {
			e.dirty = false
			ips, names, seen = append(ips, a.String()), append(names, e.name), append(seen, e.seen)
		}
	}
	return
}

// Stats reports the table size and how many lookups found a name.
func (n *Names) Stats() (size int, lookups, matched uint64) {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return len(n.byIP), n.lookups, n.matched
}

func normName(s string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(s), "."))
}

// RegisteredDomain reduces a host name to the domain it was registered under,
// e.g. "rr4---sn-abc.googlevideo.com" to "googlevideo.com". Private suffixes
// (githubusercontent.com, amazonaws.com, ...) are ignored so that traffic is
// grouped by the company operating the domain, not by each of its customers.
func RegisteredDomain(name string) string {
	name = normName(name)
	suffix, icann := publicsuffix.PublicSuffix(name)
	for !icann && strings.Contains(suffix, ".") {
		_, parent, _ := strings.Cut(suffix, ".")
		suffix, icann = publicsuffix.PublicSuffix(parent)
	}
	if name == suffix || !strings.HasSuffix(name, "."+suffix) {
		return name
	}
	rest := name[:len(name)-len(suffix)-1]
	if i := strings.LastIndexByte(rest, '.'); i >= 0 {
		rest = rest[i+1:]
	}
	return rest + "." + suffix
}
