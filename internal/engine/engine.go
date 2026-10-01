// Package engine is the core of the collector. It attributes flows to devices,
// destinations and services, stitches them into connections, keeps the live
// state, and writes rollups to the store.
package engine

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"mikrotik-home-netflow-plus/internal/alert"
	"mikrotik-home-netflow-plus/internal/enrich"
	"mikrotik-home-netflow-plus/internal/flow"
	"mikrotik-home-netflow-plus/internal/store"
)

// Options configure the engine.
type Options struct {
	FoldBelow     uint64        // connections smaller than this are not stored individually
	ConnIdle      time.Duration // a connection with no records for this long is closed
	FlushInterval time.Duration // how often pending rollups and connections are written to the store
	Retention     store.Retention
	StaticTopo    bool     // local networks and sites come from configuration, not the router
	WANInterfaces []string // configured WAN interface names; empty means discover
	APIConfigured bool
	Exporters     []netip.Addr
	FlowPort      uint16 // UDP port flow records arrive on, to hide the export stream itself
	BuildID       string // identifies the web interface build, so open pages can reload after an upgrade
}

// dict is an entry of the destination or service dictionary.
type dict struct {
	ID        int64
	Key       string
	Kind      string // destinations: domain, org, ip, site, local
	Label     string
	ASN       uint32
	Tunnel    bool // services only
	persisted bool
	queued    bool
}

// Device is something on the local network (or the router itself).
type Device struct {
	DID        int64
	ID         string // MAC address, "ip-<addr>" when the MAC is unknown, or "router"
	MAC        string
	IPs        []netip.Addr // most recent first
	Hostname   string
	Comment    string
	CustomName string
	Vendor     string
	Router     bool
	FirstSeen  time.Time
	LastSeen   time.Time

	savedSeen time.Time
	dirty     bool
	minutes   map[int64]*[2]uint64 // minute -> [down, up] bytes outside the local zone
	remotes   map[netip.Addr]int64 // remote host -> last contact (Unix seconds)
	tunnels   map[int64]bool       // tunnel services this device has used
}

// Name returns the best available display name.
func (d *Device) Name() string {
	switch {
	case d.CustomName != "":
		return d.CustomName
	case d.Comment != "":
		return d.Comment
	case d.Hostname != "":
		return d.Hostname
	case d.Router:
		return "Router"
	case d.Vendor != "" && d.Vendor != "Private address" && len(d.MAC) == 17:
		return d.Vendor + " " + d.MAC[12:]
	case len(d.IPs) > 0:
		return d.IPs[0].String()
	}
	return d.ID
}

// conn is an open connection being stitched from flow records.
type conn struct {
	key        flow.ConnKey
	id         int64 // store row id, 0 until first written
	dev        *Device
	dest       *dict
	svc        *dict
	zone       flow.Zone
	remoteName string
	first      time.Time
	last       time.Time
	lastRecv   time.Time
	up, down   uint64
	upPk       uint64
	downPk     uint64
	recent     []recentBytes // records received in the last flowWindow, for flow-derived live rates
	dirty      bool
}

// recentBytes is what one flow record added to a connection.
type recentBytes struct {
	at       time.Time
	up, down uint64
}

// flowWindow is the span flow-derived live rates are averaged over. A
// continuous flow reports about every 75 s, so a shorter window would make
// steady connections flicker in and out.
const flowWindow = 90 * time.Second

// Engine holds all in-memory state.
type Engine struct {
	opts   Options
	st     *store.Store
	topo   *flow.Topology
	names  *enrich.Names
	asn    *enrich.ASN
	alerts *alert.Manager
	log    *slog.Logger
	start  time.Time

	mu sync.Mutex

	devices  map[string]*Device // by ID
	devByDID map[int64]*Device
	ipMAC    map[netip.Addr]string
	leases   map[string][2]string // MAC -> hostname, comment
	router   *Device
	nextDID  int64

	dests    map[string]*dict
	destByID map[int64]*dict
	svcs     map[string]*dict
	svcByID  map[int64]*dict
	nextDest int64
	nextSvc  int64
	newDests []*dict
	newSvcs  []*dict

	conns      map[flow.ConnKey]*conn
	nextConnID int64

	pending    map[store.RollupKey]store.RollupVal
	merges     [][2]int64
	newTunnels []store.TunnelSeen
	wanPending map[int64]*[2]uint64 // 10-second bucket -> rx, tx bytes

	flowRing secRing // per-second WAN bytes accounted by flow records
	wanRing  secRing // per-second WAN bytes from interface counters

	lastExport   time.Time
	exportCount  uint64
	live         liveState
	lastPrune    time.Time
	uploadsCache []alert.DeviceCount
	uploadsAt    time.Time
}

// New creates an engine and loads dictionaries from the store.
func New(opts Options, st *store.Store, topo *flow.Topology, names *enrich.Names, asn *enrich.ASN, alerts *alert.Manager, log *slog.Logger) (*Engine, error) {
	if opts.ConnIdle == 0 {
		opts.ConnIdle = 150 * time.Second
	}
	if opts.FlushInterval == 0 {
		opts.FlushInterval = 20 * time.Second
	}
	e := &Engine{
		opts: opts, st: st, topo: topo, names: names, asn: asn, alerts: alerts, log: log, start: time.Now(),
		devices: map[string]*Device{}, devByDID: map[int64]*Device{}, ipMAC: map[netip.Addr]string{}, leases: map[string][2]string{},
		dests: map[string]*dict{}, destByID: map[int64]*dict{}, svcs: map[string]*dict{}, svcByID: map[int64]*dict{},
		conns: map[flow.ConnKey]*conn{}, pending: map[store.RollupKey]store.RollupVal{}, wanPending: map[int64]*[2]uint64{},
	}
	devs, err := st.LoadDevices()
	if err != nil {
		return nil, err
	}
	for _, r := range devs {
		d := &Device{DID: r.DID, ID: r.ID, MAC: r.MAC, Hostname: r.Hostname, Comment: r.Comment, CustomName: r.CustomName,
			Vendor: r.Vendor, Router: r.Router, FirstSeen: time.Unix(r.FirstSeen, 0), LastSeen: time.Unix(r.LastSeen, 0)}
		d.savedSeen = d.LastSeen
		for _, s := range strings.Split(r.IPs, ",") {
			if a, err := netip.ParseAddr(s); err == nil {
				d.IPs = append(d.IPs, a)
				if d.MAC != "" {
					e.ipMAC[a] = d.MAC
				}
			}
		}
		e.indexDevice(d)
		if d.Router {
			e.router = d
		}
		e.nextDID = max(e.nextDID, d.DID)
	}
	dests, err := st.LoadDests()
	if err != nil {
		return nil, err
	}
	for _, r := range dests {
		d := &dict{ID: r.ID, Key: r.Key, Kind: r.Kind, Label: r.Label, ASN: r.ASN, persisted: true}
		e.dests[d.Key], e.destByID[d.ID] = d, d
		e.nextDest = max(e.nextDest, d.ID)
	}
	svcs, err := st.LoadServices()
	if err != nil {
		return nil, err
	}
	for _, r := range svcs {
		d := &dict{ID: r.ID, Key: r.Label, Label: r.Label, Tunnel: r.Tunnel, persisted: true}
		e.svcs[d.Key], e.svcByID[d.ID] = d, d
		e.nextSvc = max(e.nextSvc, d.ID)
	}
	tunnels, _ := st.LoadDeviceTunnels()
	for _, t := range tunnels {
		if d := e.devByDID[t.Dev]; d != nil {
			d.tunnels[t.Svc] = true
		}
	}
	saved, _ := st.LoadDNSNames(50000)
	for _, n := range saved {
		names.Restore(n.IP, n.Name, n.Seen)
	}
	e.nextConnID = st.MaxConnID()
	if e.router == nil {
		e.router = e.newDevice("router", "", time.Now())
		e.router.Router = true
	}
	return e, nil
}

func (e *Engine) indexDevice(d *Device) {
	if d.minutes == nil {
		d.minutes, d.remotes, d.tunnels = map[int64]*[2]uint64{}, map[netip.Addr]int64{}, map[int64]bool{}
	}
	e.devices[d.ID] = d
	e.devByDID[d.DID] = d
}

func (e *Engine) newDevice(id, mac string, now time.Time) *Device {
	e.nextDID++
	d := &Device{DID: e.nextDID, ID: id, MAC: mac, FirstSeen: now, LastSeen: now, dirty: true}
	if mac != "" {
		d.Vendor = enrich.Vendor(mac)
		if l, ok := e.leases[mac]; ok {
			d.Hostname, d.Comment = l[0], l[1]
		}
	}
	e.indexDevice(d)
	return d
}

// AddFlows ingests the flows of one export message.
func (e *Engine) AddFlows(recv time.Time, flows []flow.Flow) {
	var after []func()
	e.mu.Lock()
	e.lastExport = recv
	e.exportCount++
	// Learn MACs first so that a download record processed before its upload
	// twin is still attributed to the right device.
	for i := range flows {
		if f := &flows[i]; f.MAC != "" {
			e.learnMAC(f.LocalIP, f.MAC, recv)
		}
	}
	for i := range flows {
		e.addFlow(&flows[i], recv, &after)
	}
	e.mu.Unlock()
	for _, fn := range after {
		fn()
	}
}

func (e *Engine) addFlow(f *flow.Flow, recv time.Time, after *[]func()) {
	dev := e.deviceFor(f, recv, after)
	svc := e.svcFor(enrich.LookupService(f.Proto, f.LocalPort, f.RemotePort))
	dest, name := e.destFor(f)
	if f.End.After(dev.LastSeen) {
		dev.LastSeen = f.End
	}

	key := f.Key()
	c := e.conns[key]
	if c == nil {
		c = &conn{key: key, dev: dev, dest: dest, svc: svc, zone: f.Zone, remoteName: name, first: f.Start, last: f.End}
		e.conns[key] = c
		e.addRollup(f.Start, dev, dest, svc, f.Zone, store.RollupVal{Conns: 1})
	}
	c.dev, c.dest = dev, dest // a name or MAC learned later applies from now on
	if name != "" {
		c.remoteName = name
	}
	if f.Start.Before(c.first) {
		c.first = f.Start
	}
	if f.End.After(c.last) {
		c.last = f.End
	}
	c.lastRecv, c.dirty = recv, true
	keep := c.recent[:0]
	for _, r := range c.recent {
		if recv.Sub(r.at) <= flowWindow {
			keep = append(keep, r)
		}
	}
	c.recent = keep
	if f.Dir == flow.Up {
		c.up, c.upPk = c.up+f.Bytes, c.upPk+f.Packets
		c.recent = append(c.recent, recentBytes{at: recv, up: f.Bytes})
	} else {
		c.down, c.downPk = c.down+f.Bytes, c.downPk+f.Packets
		c.recent = append(c.recent, recentBytes{at: recv, down: f.Bytes})
	}

	// Minute rollups.
	bytes, pkts := splitter{total: f.Bytes}, splitter{total: f.Packets}
	spread(f.Start, f.End, 60, func(bucket int64, cum float64) {
		b, p := bytes.take(cum), pkts.take(cum)
		var v store.RollupVal
		if f.Dir == flow.Up {
			v.Up, v.UpPk = b, p
		} else {
			v.Down, v.DownPk = b, p
		}
		e.addRollup(time.Unix(bucket, 0), dev, dest, svc, f.Zone, v)
		if f.Zone != flow.ZoneLocal {
			m := dev.minutes[bucket]
			if m == nil {
				m = &[2]uint64{}
				dev.minutes[bucket] = m
			}
			if f.Dir == flow.Up {
				m[1] += b
			} else {
				m[0] += b
			}
		}
	})

	if f.Zone == flow.ZoneWAN {
		// Per-second accounting, for the coverage figure and the flow-derived live chart.
		bytes, pkts = splitter{total: f.Bytes}, splitter{total: f.Packets}
		nowSec := recv.Unix()
		spread(f.Start, f.End, 1, func(sec int64, cum float64) {
			b, p := bytes.take(cum), pkts.take(cum)
			if sec <= nowSec-ringLen+1 || sec > nowSec+2 {
				return
			}
			i := e.flowRing.slot(sec)
			if f.Dir == flow.Up {
				e.flowRing.up[i] += float64(b)
				e.flowRing.upPk[i] += float64(p)
			} else {
				e.flowRing.down[i] += float64(b)
				e.flowRing.downPk[i] += float64(p)
			}
		})
		if f.Dir == flow.Up && !dev.Router {
			dev.remotes[f.RemoteIP] = recv.Unix()
		}
		if svc.Tunnel && !dev.Router && !dev.tunnels[svc.ID] {
			dev.tunnels[svc.ID] = true
			e.newTunnels = append(e.newTunnels, store.TunnelSeen{Dev: dev.DID, Svc: svc.ID})
			did, devName, svcName, remote := dev.DID, dev.Name(), svc.Label, f.RemoteIP.String()
			if dest.Kind != "ip" {
				remote += " (" + dest.Label + ")"
			}
			*after = append(*after, func() { e.alerts.TunnelSeen(recv, did, devName, svcName, remote) })
		}
	}
}

func (e *Engine) addRollup(at time.Time, dev *Device, dest, svc *dict, zone flow.Zone, v store.RollupVal) {
	ts := at.Unix()
	k := store.RollupKey{TS: ts - ts%60, Dev: dev.DID, Dest: dest.ID, Svc: svc.ID, Zone: uint8(zone)}
	cur := e.pending[k]
	cur.Up, cur.Down, cur.UpPk, cur.DownPk, cur.Conns = cur.Up+v.Up, cur.Down+v.Down, cur.UpPk+v.UpPk, cur.DownPk+v.DownPk, cur.Conns+v.Conns
	e.pending[k] = cur
	e.queueDict(dest, &e.newDests)
	e.queueDict(svc, &e.newSvcs)
}

func (e *Engine) queueDict(d *dict, list *[]*dict) {
	if !d.persisted && !d.queued {
		d.queued = true
		*list = append(*list, d)
	}
}

// ---- devices ----

func (e *Engine) deviceFor(f *flow.Flow, now time.Time, after *[]func()) *Device {
	if f.Router {
		return e.router
	}
	return e.deviceByIP(f.LocalIP, now, after)
}

// deviceByIP returns the device that currently holds a local address,
// creating it on first sight.
func (e *Engine) deviceByIP(ip netip.Addr, now time.Time, after *[]func()) *Device {
	id, mac := "ip-"+ip.String(), e.ipMAC[ip]
	if mac != "" {
		id = mac
	}
	d := e.devices[id]
	if d == nil {
		d = e.newDevice(id, mac, now)
		did, name := d.DID, d.Name()
		detail := ip.String()
		if mac != "" {
			detail += " · " + mac
			if d.Vendor != "" {
				detail += " · " + d.Vendor
			}
		}
		if after != nil {
			*after = append(*after, func() { e.alerts.DeviceSeen(now, did, name, detail) })
		}
	}
	if len(d.IPs) == 0 || d.IPs[0] != ip {
		ips := []netip.Addr{ip}
		for _, a := range d.IPs {
			if a != ip && len(ips) < 4 {
				ips = append(ips, a)
			}
		}
		d.IPs, d.dirty = ips, true
	}
	return d
}

// learnMAC records that ip belongs to mac. If the address had been tracked as
// a MAC-less device, that device is folded into the real one.
func (e *Engine) learnMAC(ip netip.Addr, mac string, now time.Time) {
	if e.ipMAC[ip] == mac {
		return
	}
	e.ipMAC[ip] = mac
	old := e.devices["ip-"+ip.String()]
	if old == nil {
		return
	}
	target := e.devices[mac]
	if target == nil { // promote in place: same device, now identified
		delete(e.devices, old.ID)
		old.ID, old.MAC, old.Vendor, old.dirty = mac, mac, enrich.Vendor(mac), true
		if l, ok := e.leases[mac]; ok {
			old.Hostname, old.Comment = l[0], l[1]
		}
		e.devices[mac] = old
		return
	}
	e.mergeDevices(old, target)
}

func (e *Engine) mergeDevices(from, to *Device) {
	for k, v := range e.pending {
		if k.Dev != from.DID {
			continue
		}
		delete(e.pending, k)
		k.Dev = to.DID
		cur := e.pending[k]
		cur.Up, cur.Down, cur.UpPk, cur.DownPk, cur.Conns = cur.Up+v.Up, cur.Down+v.Down, cur.UpPk+v.UpPk, cur.DownPk+v.DownPk, cur.Conns+v.Conns
		e.pending[k] = cur
	}
	for _, c := range e.conns {
		if c.dev == from {
			c.dev, c.dirty = to, true
		}
	}
	for m, v := range from.minutes {
		if cur := to.minutes[m]; cur != nil {
			cur[0], cur[1] = cur[0]+v[0], cur[1]+v[1]
		} else {
			to.minutes[m] = v
		}
	}
	if from.FirstSeen.Before(to.FirstSeen) {
		to.FirstSeen = from.FirstSeen
	}
	if from.LastSeen.After(to.LastSeen) {
		to.LastSeen = from.LastSeen
	}
	if to.CustomName == "" {
		to.CustomName = from.CustomName
	}
	to.dirty = true
	delete(e.devices, from.ID)
	delete(e.devByDID, from.DID)
	e.merges = append(e.merges, [2]int64{from.DID, to.DID})
}

// RenameDevice sets a user-chosen name. An empty name restores the automatic one.
func (e *Engine) RenameDevice(did int64, name string) error {
	e.mu.Lock()
	d := e.devByDID[did]
	if d != nil {
		d.CustomName, d.dirty = strings.TrimSpace(name), true
	}
	e.mu.Unlock()
	if d == nil {
		return fmt.Errorf("unknown device %d", did)
	}
	return nil
}

// ---- dictionaries ----

func (e *Engine) svcFor(s enrich.Service) *dict {
	d := e.svcs[s.Label]
	if d == nil {
		e.nextSvc++
		d = &dict{ID: e.nextSvc, Key: s.Label, Label: s.Label, Tunnel: s.Tunnel}
		e.svcs[d.Key], e.svcByID[d.ID] = d, d
	}
	return d
}

func (e *Engine) dest(key, kind, label string, asn uint32) *dict {
	d := e.dests[key]
	if d == nil {
		e.nextDest++
		d = &dict{ID: e.nextDest, Key: key, Kind: kind, Label: label, ASN: asn}
		e.dests[key], e.destByID[d.ID] = d, d
	}
	return d
}

// destFor picks the destination group of a flow: the registered domain when
// the address has a DNS name, else the organisation announcing it, else the
// bare address. It also returns the DNS name when there is one.
func (e *Engine) destFor(f *flow.Flow) (*dict, string) {
	return e.destOf(f.RemoteIP, f.Zone, f.Site)
}

func (e *Engine) destOf(ip netip.Addr, zone flow.Zone, site string) (*dict, string) {
	switch zone {
	case flow.ZoneSite:
		return e.dest("site:"+site, "site", site, 0), ""
	case flow.ZoneLocal:
		switch {
		case e.topo.IsRouter(ip):
			return e.dest("local:router", "local", "Router", 0), ""
		case ip.IsMulticast() || ip == netip.AddrFrom4([4]byte{255, 255, 255, 255}) || isDirectedBroadcast(ip):
			return e.dest("local:broadcast", "local", "Broadcast / multicast", 0), ""
		}
		return e.dest("local:lan", "local", "Local network", 0), ""
	}
	if name, ok := e.names.Lookup(ip); ok {
		dom := enrich.RegisteredDomain(name)
		return e.dest("d:"+dom, "domain", dom, 0), name
	}
	if org, ok := e.asn.Lookup(ip); ok {
		return e.dest("o:"+org.Name, "org", org.Name, org.Number), ""
	}
	return e.dest("i:"+ip.String(), "ip", ip.String(), 0), ""
}

func isDirectedBroadcast(ip netip.Addr) bool {
	if !ip.Is4() {
		return false
	}
	return ip.As4()[3] == 255
}

// ---- flush ----

// Run drives periodic work until ctx is cancelled.
//
// Live views are served from memory, so the flush interval only decides how
// soon new data reaches the historical views and how much is at risk on a
// power cut. Each flush rewrites the same set of database pages, so a longer
// interval means proportionally fewer bytes written to disk.
func (e *Engine) Run(ctx context.Context) {
	flush := time.NewTicker(e.opts.FlushInterval)
	eval := time.NewTicker(15 * time.Second)
	defer flush.Stop()
	defer eval.Stop()
	for {
		select {
		case <-ctx.Done():
			e.Flush(time.Now())
			return
		case now := <-flush.C:
			e.Flush(now)
			if now.Sub(e.lastPrune) > time.Hour && now.Sub(e.start) > time.Minute {
				e.prune(ctx, now)
			}
		case now := <-eval.C:
			e.alerts.Evaluate(now, e)
		}
	}
}

// Flush writes everything accumulated since the last flush.
func (e *Engine) Flush(now time.Time) {
	e.mu.Lock()
	b := &store.Batch{Now: now.Unix(), Rollups: e.pending, Merges: e.merges, Tunnels: e.newTunnels}
	e.pending, e.merges, e.newTunnels = map[store.RollupKey]store.RollupVal{}, nil, nil

	for _, d := range e.devices {
		if d.LastSeen.Sub(d.savedSeen) > time.Minute {
			d.dirty = true
		}
		for m := range d.minutes {
			if m < now.Unix()-2*3600 {
				delete(d.minutes, m)
			}
		}
		for ip, seen := range d.remotes {
			if seen < now.Unix()-900 {
				delete(d.remotes, ip)
			}
		}
		if !d.dirty {
			continue
		}
		d.dirty, d.savedSeen = false, d.LastSeen
		ips := make([]string, len(d.IPs))
		for i, a := range d.IPs {
			ips[i] = a.String()
		}
		b.Devices = append(b.Devices, store.DeviceRow{DID: d.DID, ID: d.ID, MAC: d.MAC, Hostname: d.Hostname, Comment: d.Comment,
			CustomName: d.CustomName, Vendor: d.Vendor, IPs: strings.Join(ips, ","), Router: d.Router,
			FirstSeen: d.FirstSeen.Unix(), LastSeen: d.LastSeen.Unix()})
	}

	for key, c := range e.conns {
		if c.dirty && c.up+c.down >= e.opts.FoldBelow {
			if c.id == 0 {
				e.nextConnID++
				c.id = e.nextConnID
			}
			c.dirty = false
			e.queueDict(c.dest, &e.newDests)
			e.queueDict(c.svc, &e.newSvcs)
			b.Conns = append(b.Conns, store.ConnRow{ID: c.id, FirstTS: c.first.Unix(), LastTS: c.last.Unix(), Dev: c.dev.DID,
				LocalIP: key.LocalIP.String(), LocalPort: key.LocalPort, RemoteIP: key.RemoteIP.String(), RemotePort: key.RemotePort,
				Proto: key.Proto, Svc: c.svc.ID, Dest: c.dest.ID, Zone: uint8(c.zone), RemoteName: c.remoteName,
				Up: c.up, Down: c.down, UpPk: c.upPk, DownPk: c.downPk})
		}
		if now.Sub(c.lastRecv) > e.opts.ConnIdle {
			delete(e.conns, key)
		}
	}

	dests, svcs := e.newDests, e.newSvcs
	e.newDests, e.newSvcs = nil, nil
	for _, d := range dests {
		b.Dests = append(b.Dests, store.DestRow{ID: d.ID, Key: d.Key, Kind: d.Kind, Label: d.Label, ASN: d.ASN})
	}
	for _, d := range svcs {
		b.Services = append(b.Services, store.ServiceRow{ID: d.ID, Label: d.Label, Tunnel: d.Tunnel})
	}
	cutoff := now.Unix() - now.Unix()%10 // only completed 10-second buckets
	for ts, v := range e.wanPending {
		if ts < cutoff {
			b.Wan = append(b.Wan, store.WanSample{TS: ts, Rx: v[0], Tx: v[1]})
			delete(e.wanPending, ts)
		}
	}
	e.mu.Unlock()

	ips, names, seen := e.names.Dirty()
	for i := range ips {
		b.DNS = append(b.DNS, store.DNSName{IP: ips[i], Name: names[i], Seen: seen[i]})
	}

	err := e.st.Write(b)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, d := range append(dests, svcs...) {
		d.queued = false
		d.persisted = err == nil
	}
	if err != nil {
		e.log.Error("store write failed; keeping rollups for the next attempt", "err", err)
		for k, v := range b.Rollups {
			cur := e.pending[k]
			cur.Up, cur.Down, cur.UpPk, cur.DownPk, cur.Conns = cur.Up+v.Up, cur.Down+v.Down, cur.UpPk+v.UpPk, cur.DownPk+v.DownPk, cur.Conns+v.Conns
			e.pending[k] = cur
		}
		e.merges = append(b.Merges, e.merges...)
		for _, d := range dests {
			e.queueDict(d, &e.newDests)
		}
		for _, d := range svcs {
			e.queueDict(d, &e.newSvcs)
		}
	}
}

func (e *Engine) prune(ctx context.Context, now time.Time) {
	e.lastPrune = now
	if err := e.st.Prune(ctx, e.opts.Retention, now); err != nil {
		e.log.Error("prune failed", "err", err)
		return
	}
	// Pruning drops destinations nothing refers to; make sure any that are
	// used again get written again.
	e.mu.Lock()
	for _, d := range e.dests {
		d.persisted = false
	}
	e.mu.Unlock()
}

// ---- alert.Source ----

// DeviceRates returns, per device, the lowest one-minute average rate over
// the last `minutes` complete minutes. The two most recent minutes are
// skipped because their flow records may not have arrived yet.
func (e *Engine) DeviceRates(minutes int) []alert.DeviceRate {
	e.mu.Lock()
	defer e.mu.Unlock()
	end := time.Now().Unix()/60*60 - 120
	var out []alert.DeviceRate
	for _, d := range e.devices {
		lowest := -1.0
		for i := 0; i < minutes; i++ {
			var bps float64
			if m := d.minutes[end-int64(i)*60]; m != nil {
				bps = float64(m[0]+m[1]) * 8 / 60
			}
			if lowest < 0 || bps < lowest {
				lowest = bps
			}
		}
		if lowest > 0 {
			out = append(out, alert.DeviceRate{Dev: d.DID, Name: d.Name(), MinBps: lowest})
		}
	}
	return out
}

// DeviceFanout returns how many distinct internet hosts each device contacted
// within the window.
func (e *Engine) DeviceFanout(window time.Duration) []alert.DeviceCount {
	e.mu.Lock()
	defer e.mu.Unlock()
	since := time.Now().Add(-window).Unix()
	var out []alert.DeviceCount
	for _, d := range e.devices {
		n := uint64(0)
		for _, seen := range d.remotes {
			if seen >= since {
				n++
			}
		}
		if n > 0 {
			out = append(out, alert.DeviceCount{Dev: d.DID, Name: d.Name(), Count: n})
		}
	}
	return out
}

// DeviceUploads returns bytes uploaded to the internet per device since a time.
func (e *Engine) DeviceUploads(since time.Time) []alert.DeviceCount {
	e.mu.Lock()
	if time.Since(e.uploadsAt) < time.Minute {
		out := e.uploadsCache
		e.mu.Unlock()
		return out
	}
	e.mu.Unlock()
	now := time.Now()
	rows, err := e.st.Top(store.Filter{From: since.Unix(), To: now.Unix() + 60, Zones: []uint8{uint8(flow.ZoneWAN)},
		Hourly: now.Sub(since) > 26*time.Hour}, "dev", 1000)
	if err != nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []alert.DeviceCount
	for _, r := range rows {
		if d := e.devByDID[r.ID]; d != nil && !d.Router {
			out = append(out, alert.DeviceCount{Dev: d.DID, Name: d.Name(), Count: r.Up})
		}
	}
	e.uploadsCache, e.uploadsAt = out, now
	return out
}

// Health reports the collector's own state.
func (e *Engine) Health() alert.Health {
	e.mu.Lock()
	defer e.mu.Unlock()
	cov := e.coverageLocked(time.Now())
	h := alert.Health{LastExport: e.lastExport, APIConfigured: e.opts.APIConfigured, APIUp: e.live.apiUp,
		APIDownSince: e.live.apiDownSince, Coverage: -1, Started: e.start}
	if cov.Known {
		h.Coverage = cov.Total
	}
	return h
}

// sortedDevices returns devices ordered by name, for stable output.
func (e *Engine) sortedDevices() []*Device {
	out := make([]*Device, 0, len(e.devices))
	for _, d := range e.devices {
		out = append(out, d)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name()) < strings.ToLower(out[j].Name()) })
	return out
}
