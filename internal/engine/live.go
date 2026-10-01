package engine

import (
	"math"
	"net/netip"
	"sort"
	"strings"
	"time"

	"mikrotik-home-netflow-plus/internal/enrich"
	"mikrotik-home-netflow-plus/internal/flow"
	"mikrotik-home-netflow-plus/internal/routeros"
)

const ethHeader = 14 // interface counters include the Ethernet header, flow records do not

type ifaceState struct {
	routeros.Interface
	at           time.Time
	rxBps, txBps float64
}

// liveState is everything learned from the router API.
type liveState struct {
	apiUp        bool
	apiErr       string
	apiSince     time.Time
	apiDownSince time.Time
	ifaces       map[string]*ifaceState
	wan          map[string]bool
	conns        []routeros.Conn
	connsAt      time.Time
	connTotal    int
	info         routeros.Info
	infoAt       time.Time
	wanDown      float64 // bits per second from the last counter poll
	wanUp        float64
	wanDownS     float64 // the same, smoothed for display (see smoothRate)
	wanUpS       float64
	wanAt        time.Time
	rates        map[flow.ConnKey]*liveRate // per-connection smoothed rates
	activeCount  int                        // connections in the last snapshot
}

// liveRate is the displayed state of one connection: the last row the router
// gave us plus rates smoothed across polls.
type liveRate struct {
	row  LiveRow
	down float64
	up   float64
	seen time.Time // last snapshot that contained the connection
}

// Displayed rates follow a rise quickly and a fall slowly, like a VU meter:
// a new transfer shows up within a couple of seconds, while the one-second
// burstiness of TCP is averaged away. Charts still get the raw samples.
const (
	attackTau  = 2.0 // seconds; time constant when the rate is rising
	releaseTau = 6.0 // seconds; time constant when it is falling
	rateFloor  = 2000.0
)

// smoothRate moves y towards x over dt seconds with the attack or release constant.
func smoothRate(y, x, dt float64) float64 {
	if dt <= 0 {
		return y
	}
	tau := releaseTau
	if x > y {
		tau = attackTau
	}
	return y + (1-math.Exp(-dt/tau))*(x-y)
}

// ---- routeros.Sink ----

// APIState records whether the router API session is up.
func (e *Engine) APIState(up bool, errText string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	if up && !e.live.apiUp {
		e.live.apiSince, e.live.apiDownSince = now, time.Time{}
	}
	if !up && (e.live.apiUp || e.live.apiDownSince.IsZero()) {
		e.live.apiDownSince = now
	}
	e.live.apiUp, e.live.apiErr = up, errText
	if !up { // counters are not comparable across sessions
		e.live.ifaces = nil
	}
}

// Interfaces receives interface byte counters, about once a second.
func (e *Engine) Interfaces(at time.Time, ifs []routeros.Interface) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.live.ifaces == nil {
		e.live.ifaces = map[string]*ifaceState{}
	}
	var rx, tx uint64
	var from time.Time
	for _, in := range ifs {
		st := e.live.ifaces[in.Name]
		if st == nil {
			e.live.ifaces[in.Name] = &ifaceState{Interface: in, at: at}
			continue
		}
		dt := at.Sub(st.at).Seconds()
		if dt > 0 && in.RxBytes >= st.RxBytes && in.TxBytes >= st.TxBytes {
			dr, dx := in.RxBytes-st.RxBytes, in.TxBytes-st.TxBytes
			st.rxBps, st.txBps = float64(dr)*8/dt, float64(dx)*8/dt
			if e.live.wan[in.Name] {
				rx, tx, from = rx+dr, tx+dx, st.at
			}
		}
		st.Interface, st.at = in, at
	}
	if from.IsZero() {
		return
	}
	dt := at.Sub(from).Seconds()
	down, up := float64(rx)*8/dt, float64(tx)*8/dt
	if e.live.wanAt.IsZero() || at.Sub(e.live.wanAt) > 30*time.Second {
		e.live.wanDownS, e.live.wanUpS = down, up // first sample after a gap: no history to smooth from
	} else {
		e.live.wanDownS = smoothRate(e.live.wanDownS, down, dt)
		e.live.wanUpS = smoothRate(e.live.wanUpS, up, dt)
	}
	e.live.wanDown, e.live.wanUp, e.live.wanAt = down, up, at
	// Spread over the real polling interval so a late poll does not look like a spike.
	r, t := splitter{total: rx}, splitter{total: tx}
	spread(from, at, 1, func(sec int64, cum float64) {
		dr, dx := r.take(cum), t.take(cum)
		i := e.wanRing.slot(sec)
		e.wanRing.down[i] += float64(dr)
		e.wanRing.up[i] += float64(dx)
		b := sec - sec%10
		w := e.wanPending[b]
		if w == nil {
			w = &[2]uint64{}
			e.wanPending[b] = w
		}
		w[0], w[1] = w[0]+dr, w[1]+dx
	})
}

// Connections receives the connections that are moving data right now.
// Their rates are folded into per-connection smoothed values; a connection
// that drops out of the snapshot fades out instead of vanishing.
func (e *Engine) Connections(at time.Time, active []routeros.Conn, total int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.live.rates == nil {
		e.live.rates = map[flow.ConnKey]*liveRate{}
	}
	dt := 0.0
	if !e.live.connsAt.IsZero() {
		dt = at.Sub(e.live.connsAt).Seconds()
	}
	e.live.activeCount = 0
	for i := range active {
		r, key, ok := e.rowFromConntrack(&active[i])
		if !ok {
			continue
		}
		e.live.activeCount++
		lr := e.live.rates[key]
		if lr == nil || dt > 30 {
			lr = &liveRate{down: r.Down, up: r.Up} // first sight: show it at full rate at once
			e.live.rates[key] = lr
		} else {
			lr.down = smoothRate(lr.down, r.Down, dt)
			lr.up = smoothRate(lr.up, r.Up, dt)
		}
		lr.row, lr.seen = r, at
	}
	for key, lr := range e.live.rates {
		if lr.seen.Equal(at) {
			continue
		}
		lr.down = smoothRate(lr.down, 0, dt)
		lr.up = smoothRate(lr.up, 0, dt)
		if lr.down+lr.up < rateFloor || at.Sub(lr.seen) > 30*time.Second {
			delete(e.live.rates, key)
		}
	}
	e.live.conns, e.live.connsAt, e.live.connTotal = active, at, total
}

// DNSCache receives a snapshot of the router's DNS cache.
func (e *Engine) DNSCache(at time.Time, recs []enrich.DNSRecord) {
	e.names.Observe(recs, at)
}

// Neighbors receives ARP, DHCP lease and IPv6 neighbour entries.
func (e *Engine) Neighbors(at time.Time, list []routeros.Neighbor) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, n := range list {
		if !e.topo.IsLocal(n.IP) || e.topo.IsRouter(n.IP) {
			continue
		}
		e.learnMAC(n.IP, n.MAC, at)
		if n.Hostname == "" && n.Comment == "" {
			continue
		}
		e.leases[n.MAC] = [2]string{n.Hostname, n.Comment}
		if d := e.devices[n.MAC]; d != nil && (d.Hostname != n.Hostname || d.Comment != n.Comment) {
			d.Hostname, d.Comment, d.dirty = n.Hostname, n.Comment, true
		}
	}
}

// RouterInfo receives slow-changing router state and derives the topology.
func (e *Engine) RouterInfo(at time.Time, info routeros.Info) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.live.info, e.live.infoAt = info, at
	if info.Identity != "" && e.router.Hostname != info.Identity {
		e.router.Hostname, e.router.dirty = info.Identity, true
	}
	for _, p := range info.WireGuardPorts {
		enrich.RegisterPort(17, p, "WireGuard", true)
	}
	wanNames := e.opts.WANInterfaces
	if len(wanNames) == 0 {
		wanNames = info.WANInterfaces
	}
	wan := map[string]bool{}
	for _, n := range wanNames {
		wan[n] = true
	}
	e.live.wan = wan
	for _, a := range info.Addresses {
		e.topo.LearnRouter(a.Prefix.Addr().Unmap())
	}
	if e.opts.StaticTopo {
		return
	}
	_, configured, _ := e.topo.Snapshot()
	local := []netip.Prefix{netip.MustParsePrefix("fe80::/10")}
	var sites []flow.Site
	siteName := func(p netip.Prefix, iface string) string {
		for _, s := range configured {
			if s.Prefix == p {
				return s.Name
			}
		}
		return iface
	}
	seen := map[netip.Prefix]bool{local[0]: true}
	add := func(p netip.Prefix, iface string) {
		p = p.Masked()
		if wan[iface] || seen[p] || p.IsSingleIP() || p.Addr().IsLoopback() || p.Addr().IsLinkLocalUnicast() {
			return
		}
		seen[p] = true
		if st := e.live.ifaces[iface]; st != nil && isTunnelType(st.Type) {
			sites = append(sites, flow.Site{Name: siteName(p, iface), Prefix: p})
		} else {
			local = append(local, p)
		}
	}
	for _, a := range info.Addresses {
		add(a.Prefix, a.Interface)
	}
	for _, r := range info.Routes {
		if r.Dst.Bits() > 0 {
			add(r.Dst, r.Interface)
		}
	}
	for _, s := range configured { // configured sites the router does not route itself stay in place
		if !seen[s.Prefix] {
			sites = append(sites, s)
		}
	}
	e.topo.Set(local, sites)
}

func isTunnelType(t string) bool {
	for _, p := range []string{"wg", "l2tp", "sstp", "ovpn", "pptp", "gre", "ipip", "eoip", "ppp", "zerotier", "6to4", "vxlan", "ipsec"} {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// ---- live view ----

// LiveRow is one active connection as shown in the live table.
type LiveRow struct {
	Dev        int64   `json:"dev"`
	DevName    string  `json:"devName"`
	LocalIP    string  `json:"localIp"`
	LocalPort  uint16  `json:"localPort"`
	RemoteIP   string  `json:"remoteIp"`
	RemotePort uint16  `json:"remotePort"`
	Proto      string  `json:"proto"`
	Remote     string  `json:"remote"` // DNS name, when known
	Dest       int64   `json:"dest"`
	DestLabel  string  `json:"destLabel"`
	DestKind   string  `json:"destKind"`
	Service    string  `json:"service"`
	Tunnel     bool    `json:"tunnel"`
	Zone       string  `json:"zone"`
	Site       string  `json:"site,omitempty"`
	Down       float64 `json:"down"` // bits per second
	Up         float64 `json:"up"`
	BytesDown  uint64  `json:"bytesDown"`
	BytesUp    uint64  `json:"bytesUp"`
	State      string  `json:"state,omitempty"`
	svc        int64
}

// Mode says where live numbers come from: "api" (router connection table and
// interface counters, 1-2 s old) or "flows" (flow records, 15-75 s old).
func (e *Engine) modeLocked(now time.Time) string {
	if e.live.apiUp && now.Sub(e.live.connsAt) < 15*time.Second {
		return "api"
	}
	return "flows"
}

// LiveRows returns the connections that are moving data now, fastest first.
func (e *Engine) LiveRows(limit int) (rows []LiveRow, mode string, total int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	rows, mode = e.liveRowsLocked(now)
	total = len(rows)
	if limit > 0 && len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, mode, total
}

func (e *Engine) liveRowsLocked(now time.Time) ([]LiveRow, string) {
	mode := e.modeLocked(now)
	var rows []LiveRow
	if mode == "api" {
		rows = make([]LiveRow, 0, len(e.live.rates))
		for _, lr := range e.live.rates {
			r := lr.row
			r.Down, r.Up = lr.down, lr.up
			rows = append(rows, r)
		}
	} else {
		// Without the router API the best available "now" is the average over
		// the flow records that arrived in the last flowWindow.
		for key, c := range e.conns {
			var up, down uint64
			for _, rec := range c.recent {
				if now.Sub(rec.at) <= flowWindow {
					up, down = up+rec.up, down+rec.down
				}
			}
			if up == 0 && down == 0 {
				continue
			}
			r := LiveRow{BytesDown: c.down, BytesUp: c.up,
				Down: float64(down*8) / flowWindow.Seconds(), Up: float64(up*8) / flowWindow.Seconds()}
			e.fillRow(&r, c.dev, key.LocalIP, key.LocalPort, key.RemoteIP, key.RemotePort, key.Proto, c.zone, c.dest, c.remoteName)
			rows = append(rows, r)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i].Down+rows[i].Up, rows[j].Down+rows[j].Up
		if a != b {
			return a > b
		}
		return rows[i].BytesDown+rows[i].BytesUp > rows[j].BytesDown+rows[j].BytesUp
	})
	return rows, mode
}

// rowFromConntrack maps a router connection-table entry onto the inside view
// and returns the connection key the row is tracked under.
func (e *Engine) rowFromConntrack(c *routeros.Conn) (LiveRow, flow.ConnKey, bool) {
	var r LiveRow
	var key flow.ConnKey
	if c.Proto == 17 && c.DstPort == e.opts.FlowPort && e.opts.FlowPort != 0 && e.topo.IsRouter(c.Src) {
		return r, key, false // the router's flow export to a collector
	}
	var local, remote netip.Addr
	var lport, rport uint16
	var zone flow.Zone
	var site string
	router := false
	localIsSrc := true
	if c.DstNAT && c.ReplySrc.IsValid() && c.ReplySrc != c.Dst {
		// Port forward: the LAN host is the translated destination.
		local, lport, remote, rport, localIsSrc, zone = c.ReplySrc, c.ReplySrcPort, c.Src, c.SrcPort, false, flow.ZoneWAN
	} else {
		side := e.topo.Classify(c.Src, c.Dst, false, false)
		if side.Drop {
			return r, key, false
		}
		zone, site, router, localIsSrc = side.Zone, side.Site, side.Router, side.LocalIsSrc
		if localIsSrc {
			local, lport, remote, rport = c.Src, c.SrcPort, c.Dst, c.DstPort
		} else {
			local, lport, remote, rport = c.Dst, c.DstPort, c.Src, c.SrcPort
		}
	}
	if localIsSrc {
		r.Up, r.Down, r.BytesUp, r.BytesDown = float64(c.OrigRate), float64(c.ReplRate), c.OrigBytes, c.ReplBytes
	} else {
		r.Up, r.Down, r.BytesUp, r.BytesDown = float64(c.ReplRate), float64(c.OrigRate), c.ReplBytes, c.OrigBytes
	}
	var dev *Device
	if router {
		dev = e.router
	} else if mac := e.ipMAC[local]; mac != "" {
		dev = e.devices[mac]
	} else {
		dev = e.devices["ip-"+local.String()]
	}
	dest, name := e.destOf(remote, zone, site)
	r.State, r.Site = c.TCPState, site
	e.fillRow(&r, dev, local, lport, remote, rport, c.Proto, zone, dest, name)
	if c.Proto == 1 || c.Proto == 58 { // ICMP: the same key rule as the flow lane
		lport, rport = 0, 0
	}
	key = flow.ConnKey{Proto: c.Proto, LocalIP: local, LocalPort: lport, RemoteIP: remote, RemotePort: rport}
	return r, key, true
}

func (e *Engine) fillRow(r *LiveRow, dev *Device, local netip.Addr, lport uint16, remote netip.Addr, rport uint16, proto uint8, zone flow.Zone, dest *dict, name string) {
	svc := enrich.LookupService(proto, lport, rport)
	r.LocalIP, r.LocalPort, r.RemoteIP, r.RemotePort = local.String(), lport, remote.String(), rport
	r.Proto, r.Service, r.Tunnel, r.Zone, r.Remote = enrich.ProtoName(proto), svc.Label, svc.Tunnel, zone.String(), name
	r.Dest, r.DestLabel, r.DestKind = dest.ID, dest.Label, dest.Kind
	r.svc = e.svcFor(svc).ID
	if dev != nil {
		r.Dev, r.DevName = dev.DID, dev.Name()
	} else {
		r.DevName = local.String()
	}
}

// RateItem is one entry of a live ranking.
type RateItem struct {
	ID    int64   `json:"id"`
	Label string  `json:"label"`
	Kind  string  `json:"kind,omitempty"`
	Down  float64 `json:"down"`
	Up    float64 `json:"up"`
	Conns int     `json:"conns"`
}

// Tick is the once-a-second live update pushed to browsers.
type Tick struct {
	TS            int64        `json:"ts"` // Unix milliseconds
	Mode          string       `json:"mode"`
	Down          float64      `json:"down"` // WAN bits per second
	Up            float64      `json:"up"`
	Tail          [][3]float64 `json:"tail"` // recent [second, down bps, up bps] samples
	Conns         int          `json:"conns"`
	Active        int          `json:"active"`
	Devices       int          `json:"devices"`
	TopDevices    []RateItem   `json:"topDevices"`
	TopDests      []RateItem   `json:"topDests"`
	TopServices   []RateItem   `json:"topServices"`
	Coverage      *float64     `json:"coverage"`
	ExportAge     float64      `json:"exportAge"` // seconds since the last flow export, -1 if none yet
	Firing        int          `json:"firing"`
	Unacked       int          `json:"unacked"`
	AlertTitle    string       `json:"alertTitle,omitempty"` // the firing alert that matters most
	AlertSeverity string       `json:"alertSeverity,omitempty"`
	Build         string       `json:"build"`
	RouterName    string       `json:"routerName"`
	APIError      string       `json:"apiError,omitempty"`
	APIConfigured bool         `json:"apiConfigured"`
}

// Tick builds the current live update.
func (e *Engine) Tick() Tick {
	now := time.Now()
	unacked := e.st.UnackedCount()
	alertTitle, alertSeverity, firing := e.alerts.MostSevere()
	e.mu.Lock()
	defer e.mu.Unlock()
	rows, mode := e.liveRowsLocked(now)
	active := len(rows)
	if mode == "api" {
		active = e.live.activeCount
	}
	t := Tick{TS: now.UnixMilli(), Mode: mode, Active: active, Conns: e.live.connTotal, ExportAge: -1,
		Firing: firing, Unacked: unacked, AlertTitle: alertTitle, AlertSeverity: alertSeverity, Build: e.opts.BuildID, RouterName: e.router.Name(), APIConfigured: e.opts.APIConfigured, APIError: e.live.apiErr}
	if !e.lastExport.IsZero() {
		t.ExportAge = now.Sub(e.lastExport).Seconds()
	}
	sec := now.Unix()
	if mode == "api" && now.Sub(e.live.wanAt) < 5*time.Second {
		t.Down, t.Up = e.live.wanDownS, e.live.wanUpS // smoothed for the figures; the tail stays raw for the chart
		t.Tail = e.tailLocked(&e.wanRing, sec-6, sec-1)
	} else {
		t.Mode = "flows"
		// Flow records trail reality; report the most recent window that is mostly complete.
		t.Tail = e.tailLocked(&e.flowRing, sec-180, sec-1)
		for i := range rows { // the same 90-second average the rankings use
			if rows[i].Zone == "wan" {
				t.Down, t.Up = t.Down+rows[i].Down, t.Up+rows[i].Up
			}
		}
		t.Conns = len(e.conns)
	}
	if t.Conns <= 0 {
		t.Conns = len(e.conns)
	}
	for _, d := range e.devices {
		if now.Sub(d.LastSeen) < 5*time.Minute && !d.Router {
			t.Devices++
		}
	}
	// More candidates than the interface shows, so that it can keep rows in a
	// stable order and only promote one when it is clearly ahead.
	t.TopDevices, t.TopDests, t.TopServices = rankRows(rows, 24)
	if cov := e.coverageLocked(now); cov.Known {
		t.Coverage = &cov.Total
	}
	return t
}

// tailLocked returns [second, down bps, up bps] for seconds in [from, to].
func (e *Engine) tailLocked(r *secRing, from, to int64) [][3]float64 {
	out := make([][3]float64, 0, to-from+1)
	for s := from; s <= to; s++ {
		if i, ok := r.at(s); ok {
			out = append(out, [3]float64{float64(s), r.down[i] * 8, r.up[i] * 8})
		} else {
			out = append(out, [3]float64{float64(s), 0, 0})
		}
	}
	return out
}

// LiveSeries returns per-second WAN rates for the last `seconds` seconds.
func (e *Engine) LiveSeries(seconds int64) (series [][3]float64, mode string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	sec := now.Unix()
	seconds = min(seconds, ringLen-5)
	if e.modeLocked(now) == "api" && now.Sub(e.live.wanAt) < 5*time.Second {
		return e.tailLocked(&e.wanRing, sec-seconds, sec-1), "api"
	}
	return e.tailLocked(&e.flowRing, sec-seconds, sec-1), "flows"
}

// rankRows aggregates live rows by device, destination and service. Traffic
// that stays on the local network is left out.
func rankRows(rows []LiveRow, n int) (devs, dests, svcs []RateItem) {
	type agg = map[int64]*RateItem
	byDev, byDest, bySvc := agg{}, agg{}, agg{}
	add := func(m agg, id int64, label, kind string, r *LiveRow) {
		it := m[id]
		if it == nil {
			it = &RateItem{ID: id, Label: label, Kind: kind}
			m[id] = it
		}
		it.Down, it.Up, it.Conns = it.Down+r.Down, it.Up+r.Up, it.Conns+1
	}
	for i := range rows {
		r := &rows[i]
		if r.Zone == "local" {
			continue
		}
		add(byDev, r.Dev, r.DevName, "", r)
		add(byDest, r.Dest, r.DestLabel, r.DestKind, r)
		add(bySvc, r.svc, r.Service, "", r)
	}
	top := func(m agg) []RateItem {
		out := make([]RateItem, 0, len(m))
		for _, it := range m {
			out = append(out, *it)
		}
		sort.Slice(out, func(i, j int) bool {
			a, b := out[i].Down+out[i].Up, out[j].Down+out[j].Up
			if a != b {
				return a > b
			}
			return out[i].Label < out[j].Label
		})
		if len(out) > n {
			out = out[:n]
		}
		return out
	}
	return top(byDev), top(byDest), top(bySvc)
}

// Coverage compares flow-accounted WAN bytes with the WAN interface counters.
type Coverage struct {
	Known   bool    `json:"known"`
	Total   float64 `json:"total"` // fraction of counter bytes accounted for by flow records
	Down    float64 `json:"down"`
	Up      float64 `json:"up"`
	Seconds int     `json:"seconds"` // size of the comparison window
}

// coverageLocked compares the two lanes over a window that ends 90 s ago, so
// that flow records for it have had time to arrive.
func (e *Engine) coverageLocked(now time.Time) Coverage {
	var fd, fu, wd, wu float64
	n := 0
	for sec := now.Unix() - 690; sec < now.Unix()-90; sec++ {
		wi, ok := e.wanRing.at(sec)
		if !ok {
			continue
		}
		n++
		wd, wu = wd+e.wanRing.down[wi], wu+e.wanRing.up[wi]
		if fi, ok := e.flowRing.at(sec); ok {
			fd += e.flowRing.down[fi] + ethHeader*e.flowRing.downPk[fi]
			fu += e.flowRing.up[fi] + ethHeader*e.flowRing.upPk[fi]
		}
	}
	c := Coverage{Seconds: n}
	if n < 180 || wd+wu < 2e6 || e.lastExport.IsZero() { // too little to judge
		return c
	}
	c.Known, c.Total = true, (fd+fu)/(wd+wu)
	if wd > 0 {
		c.Down = fd / wd
	}
	if wu > 0 {
		c.Up = fu / wu
	}
	return c
}
