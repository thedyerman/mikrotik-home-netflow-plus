package flow

import (
	"net/netip"
	"sort"
	"sync"
)

// Site is a named remote network reached through the router (e.g. a tunnel).
type Site struct {
	Name   string       `json:"name"`
	Prefix netip.Prefix `json:"prefix"`
}

// Topology knows which addresses are local, which belong to a remote site and
// which are the router's own. It is shared by the flow and live lanes and can
// be updated at runtime from the router API.
type Topology struct {
	mu     sync.RWMutex
	local  []netip.Prefix
	sites  []Site
	router map[netip.Addr]struct{}
	// Candidate router addresses: sources seen with ingress interface 0 and
	// destinations seen with egress interface 0.
	fromZero, toZero map[netip.Addr]struct{}
}

// NewTopology creates a topology from static configuration.
func NewTopology(local []netip.Prefix, sites []Site, routerAddrs []netip.Addr) *Topology {
	t := &Topology{router: map[netip.Addr]struct{}{}, fromZero: map[netip.Addr]struct{}{}, toZero: map[netip.Addr]struct{}{}}
	t.Set(local, sites)
	for _, a := range routerAddrs {
		t.router[a.Unmap()] = struct{}{}
	}
	return t
}

// Set replaces the local and site networks.
func (t *Topology) Set(local []netip.Prefix, sites []Site) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.local = append([]netip.Prefix(nil), local...)
	t.sites = append([]Site(nil), sites...)
}

// Snapshot returns the current networks and router addresses.
func (t *Topology) Snapshot() (local []netip.Prefix, sites []Site, router []netip.Addr) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	local = append(local, t.local...)
	sites = append(sites, t.sites...)
	for a := range t.router {
		router = append(router, a)
	}
	sort.Slice(router, func(i, j int) bool { return router[i].Less(router[j]) })
	return
}

// SiteOf returns the site an address belongs to.
func (t *Topology) SiteOf(a netip.Addr) (string, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, s := range t.sites {
		if s.Prefix.Contains(a) {
			return s.Name, true
		}
	}
	return "", false
}

// IsLocal reports whether a is on a local network and not part of a site.
func (t *Topology) IsLocal(a netip.Addr) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	for _, s := range t.sites {
		if s.Prefix.Contains(a) {
			return false
		}
	}
	for _, p := range t.local {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// IsRouter reports whether a is one of the router's own addresses.
func (t *Topology) IsRouter(a netip.Addr) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	_, ok := t.router[a]
	return ok
}

// LearnRouter records a as a router address. It returns true if it was new.
func (t *Topology) LearnRouter(a netip.Addr) bool {
	if !a.IsValid() || a.IsUnspecified() || a.IsMulticast() || isBroadcast(a) {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, ok := t.router[a]; ok {
		return false
	}
	if len(t.router) >= 64 { // a router has a handful of addresses; refuse to grow without bound
		return false
	}
	t.router[a] = struct{}{}
	return true
}

// ObserveLocalDelivery learns router addresses from flow records without
// needing the router API. An address is the router's own when it has been
// seen both as a source entering from interface 0 and as a destination
// leaving to interface 0: remote hosts never satisfy the second condition and
// broadcast addresses never satisfy the first.
func (t *Topology) ObserveLocalDelivery(src netip.Addr, srcZero bool, dst netip.Addr, dstZero bool) {
	if !srcZero && !dstZero {
		return
	}
	const maxCandidates = 512
	t.mu.Lock()
	var learn netip.Addr
	if srcZero {
		if _, known := t.router[src]; !known {
			if _, ok := t.toZero[src]; ok {
				learn = src
			} else if len(t.fromZero) < maxCandidates {
				t.fromZero[src] = struct{}{}
			}
		}
	}
	if dstZero {
		if _, known := t.router[dst]; !known {
			if _, ok := t.fromZero[dst]; ok {
				learn = dst
			} else if len(t.toZero) < maxCandidates {
				t.toZero[dst] = struct{}{}
			}
		}
	}
	t.mu.Unlock()
	if learn.IsValid() && t.LearnRouter(learn) {
		t.mu.Lock()
		delete(t.fromZero, learn)
		delete(t.toZero, learn)
		t.mu.Unlock()
	}
}

func isBroadcast(a netip.Addr) bool {
	if !a.Is4() {
		return false
	}
	b := a.As4()
	return b == [4]byte{255, 255, 255, 255}
}

// Side describes how an un-NATed address pair maps onto local and remote.
type Side struct {
	LocalIsSrc bool
	Zone       Zone
	Site       string
	Router     bool // the local side is the router itself
	Drop       bool // router talking to itself
}

// Classify decides which end of src->dst is the local device. srcRouter and
// dstRouter are extra hints (for example interface 0 in a flow record, or a
// broadcast destination) on top of the known router addresses.
func (t *Topology) Classify(src, dst netip.Addr, srcRouter, dstRouter bool) Side {
	srcRouter = srcRouter || t.IsRouter(src)
	dstRouter = dstRouter || t.IsRouter(dst) || dst.IsMulticast() || isBroadcast(dst)
	srcLocal, dstLocal := t.IsLocal(src), t.IsLocal(dst)
	far := func(a netip.Addr) (Zone, string) {
		if name, ok := t.SiteOf(a); ok {
			return ZoneSite, name
		}
		return ZoneWAN, ""
	}
	switch {
	case srcRouter && dstRouter:
		return Side{Drop: true}
	case srcRouter:
		if dstLocal {
			return Side{LocalIsSrc: false, Zone: ZoneLocal}
		}
		z, s := far(dst)
		return Side{LocalIsSrc: true, Zone: z, Site: s, Router: true}
	case dstRouter:
		if srcLocal {
			return Side{LocalIsSrc: true, Zone: ZoneLocal}
		}
		z, s := far(src)
		return Side{LocalIsSrc: false, Zone: z, Site: s, Router: true}
	case srcLocal && dstLocal:
		return Side{LocalIsSrc: true, Zone: ZoneLocal}
	case srcLocal:
		z, s := far(dst)
		return Side{LocalIsSrc: true, Zone: z, Site: s}
	case dstLocal:
		z, s := far(src)
		return Side{LocalIsSrc: false, Zone: z, Site: s}
	default: // transit traffic between two non-local networks: attribute to the router
		z, s := far(dst)
		return Side{LocalIsSrc: true, Zone: z, Site: s, Router: true}
	}
}
