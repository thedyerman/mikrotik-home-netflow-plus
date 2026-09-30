package engine

import (
	"fmt"
	"sort"
	"time"

	"mikrotik-home-netflow-plus/internal/enrich"
	"mikrotik-home-netflow-plus/internal/flow"
	"mikrotik-home-netflow-plus/internal/store"
)

// Range is a named time window with the bucket sizes used to chart it.
type Range struct {
	Name     string
	Seconds  int64
	WanStep  int64 // bucket size when charting interface counters (0: not available)
	RollStep int64 // bucket size when charting flow rollups
	Hourly   bool  // read the hourly rollup table
}

var ranges = []Range{
	{"15m", 900, 10, 60, false},
	{"1h", 3600, 30, 60, false},
	{"6h", 6 * 3600, 120, 120, false},
	{"24h", 24 * 3600, 300, 300, false},
	{"7d", 7 * 86400, 1800, 3600, true},
	{"30d", 30 * 86400, 0, 4 * 3600, true},
}

// ParseRange looks up a range by name, defaulting to one hour.
func ParseRange(name string) Range {
	for _, r := range ranges {
		if r.Name == name {
			return r
		}
	}
	return ranges[1]
}

// ParseZones maps a zone selector to zone numbers. "external" (the default)
// is everything that leaves the local network: internet plus remote sites.
func ParseZones(name string) []uint8 {
	switch name {
	case "wan":
		return []uint8{uint8(flow.ZoneWAN)}
	case "site":
		return []uint8{uint8(flow.ZoneSite)}
	case "local":
		return []uint8{uint8(flow.ZoneLocal)}
	case "all":
		return nil
	}
	return []uint8{uint8(flow.ZoneWAN), uint8(flow.ZoneSite)}
}

func (r Range) filter(now time.Time, zones []uint8) store.Filter {
	to := now.Unix() + 1
	return store.Filter{From: to - r.Seconds, To: to, Zones: zones, Hourly: r.Hourly}
}

// VolItem is one entry of a ranking by volume.
type VolItem struct {
	ID     int64  `json:"id"`
	Label  string `json:"label"`
	Kind   string `json:"kind,omitempty"`
	Tunnel bool   `json:"tunnel,omitempty"`
	Down   uint64 `json:"down"` // bytes
	Up     uint64 `json:"up"`
	Conns  uint64 `json:"conns"`
}

// Overview is the data behind the dashboard for a historical range.
type Overview struct {
	Range       string       `json:"range"`
	From        int64        `json:"from"`
	To          int64        `json:"to"`
	Step        int64        `json:"step"`
	Source      string       `json:"source"` // "wan": interface counters, "flows": flow records
	Series      [][3]float64 `json:"series"` // [second, down bps, up bps]
	Down        uint64       `json:"down"`   // bytes in range
	Up          uint64       `json:"up"`
	Conns       uint64       `json:"conns"`
	PeakDown    float64      `json:"peakDown"` // bits per second
	PeakUp      float64      `json:"peakUp"`
	TopDevices  []VolItem    `json:"topDevices"`
	TopDests    []VolItem    `json:"topDests"`
	TopServices []VolItem    `json:"topServices"`
}

// Overview builds the dashboard data for a range and zone selection.
func (e *Engine) Overview(rangeName, zoneName string, dev int64) (*Overview, error) {
	now := time.Now()
	r := ParseRange(rangeName)
	f := r.filter(now, ParseZones(zoneName))
	f.Dev = dev
	o := &Overview{Range: r.Name, From: f.From, To: f.To}

	// Interface counters are exact and current, so prefer them for the chart
	// when the view is of internet traffic as a whole.
	useWan := dev == 0 && r.WanStep > 0 && (zoneName == "" || zoneName == "external" || zoneName == "wan")
	var pts []store.Point
	var err error
	if useWan {
		if pts, err = e.st.WanSeries(f.From, f.To, r.WanStep); err != nil {
			return nil, err
		}
		if int64(len(pts)) < r.Seconds/r.WanStep/3 { // counters cover too little of the range
			useWan = false
		}
	}
	if useWan {
		o.Step, o.Source = r.WanStep, "wan"
	} else {
		o.Step, o.Source = r.RollStep, "flows"
		if pts, err = e.st.Series(f, o.Step); err != nil {
			return nil, err
		}
	}
	o.Series, o.PeakDown, o.PeakUp = fillSeries(pts, f.From, f.To, o.Step)

	tot, err := e.st.Totals(f)
	if err != nil {
		return nil, err
	}
	o.Down, o.Up, o.Conns = tot.Down, tot.Up, tot.Conns
	if o.TopDevices, err = e.top(f, "dev", 10); err != nil {
		return nil, err
	}
	if o.TopDests, err = e.top(f, "dest", 10); err != nil {
		return nil, err
	}
	if o.TopServices, err = e.top(f, "svc", 10); err != nil {
		return nil, err
	}
	return o, nil
}

// fillSeries turns sparse byte buckets into a gap-free series of rates.
func fillSeries(pts []store.Point, from, to, step int64) (series [][3]float64, peakDown, peakUp float64) {
	byTS := make(map[int64]store.Point, len(pts))
	for _, p := range pts {
		byTS[p.TS] = p
	}
	first := from / step * step
	series = make([][3]float64, 0, (to-first)/step+1)
	for ts := first; ts < to; ts += step {
		p := byTS[ts]
		d, u := float64(p.Down)*8/float64(step), float64(p.Up)*8/float64(step)
		series = append(series, [3]float64{float64(ts), d, u})
		peakDown, peakUp = max(peakDown, d), max(peakUp, u)
	}
	return
}

func (e *Engine) top(f store.Filter, by string, n int) ([]VolItem, error) {
	rows, err := e.st.Top(f, by, n)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]VolItem, 0, len(rows))
	for _, r := range rows {
		it := VolItem{ID: r.ID, Down: r.Down, Up: r.Up, Conns: r.Conns}
		switch by {
		case "dev":
			it.Label = e.devName(r.ID)
		case "dest":
			it.Label, it.Kind = e.destLabel(r.ID)
		case "svc":
			if s := e.svcByID[r.ID]; s != nil {
				it.Label, it.Tunnel = s.Label, s.Tunnel
			} else {
				it.Label = fmt.Sprintf("service %d", r.ID)
			}
		}
		out = append(out, it)
	}
	return out, nil
}

func (e *Engine) devName(did int64) string {
	if d := e.devByDID[did]; d != nil {
		return d.Name()
	}
	return fmt.Sprintf("device %d", did)
}

func (e *Engine) destLabel(id int64) (label, kind string) {
	if d := e.destByID[id]; d != nil {
		return d.Label, d.Kind
	}
	return fmt.Sprintf("destination %d", id), "ip"
}

// DeviceInfo describes a device for the list and detail views.
type DeviceInfo struct {
	ID         int64     `json:"id"`
	Name       string    `json:"name"`
	CustomName string    `json:"customName"`
	Hostname   string    `json:"hostname"`
	MAC        string    `json:"mac"`
	Vendor     string    `json:"vendor"`
	IPs        []string  `json:"ips"`
	Router     bool      `json:"router"`
	FirstSeen  int64     `json:"firstSeen"`
	LastSeen   int64     `json:"lastSeen"`
	Down       uint64    `json:"down"` // bytes in range
	Up         uint64    `json:"up"`
	Conns      uint64    `json:"conns"`
	LiveDown   float64   `json:"liveDown"` // bits per second now
	LiveUp     float64   `json:"liveUp"`
	LiveConns  int       `json:"liveConns"`
	Spark      []float64 `json:"spark"` // total bytes per bucket across the range
}

func (e *Engine) deviceInfo(d *Device) DeviceInfo {
	info := DeviceInfo{ID: d.DID, Name: d.Name(), CustomName: d.CustomName, Hostname: d.Hostname, MAC: d.MAC, Vendor: d.Vendor,
		Router: d.Router, FirstSeen: d.FirstSeen.Unix(), LastSeen: d.LastSeen.Unix(), IPs: []string{}}
	for _, a := range d.IPs {
		info.IPs = append(info.IPs, a.String())
	}
	return info
}

const sparkBuckets = 48

// Devices lists every device with its volume over the range and its live rate.
func (e *Engine) Devices(rangeName string) ([]DeviceInfo, error) {
	now := time.Now()
	r := ParseRange(rangeName)
	f := r.filter(now, ParseZones("external"))
	totals, err := e.st.Top(f, "dev", 10000)
	if err != nil {
		return nil, err
	}
	step := max(r.Seconds/sparkBuckets, 60)
	if r.Hourly {
		step = max(step/3600*3600, 3600)
	} else {
		step = step / 60 * 60
	}
	series, err := e.st.DeviceSeries(f, step)
	if err != nil {
		return nil, err
	}
	first := f.From / step * step
	n := int((f.To-first)/step) + 1
	sparks := map[int64][]float64{}
	for _, p := range series {
		s := sparks[p.Dev]
		if s == nil {
			s = make([]float64, n)
			sparks[p.Dev] = s
		}
		if i := int((p.TS - first) / step); i >= 0 && i < n {
			s[i] = float64(p.Down + p.Up)
		}
	}
	byDev := map[int64]store.TopRow{}
	for _, t := range totals {
		byDev[t.ID] = t
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	rows, _ := e.liveRowsLocked(now)
	live := map[int64]*RateItem{}
	for i := range rows {
		if rows[i].Zone == "local" {
			continue
		}
		it := live[rows[i].Dev]
		if it == nil {
			it = &RateItem{}
			live[rows[i].Dev] = it
		}
		it.Down, it.Up, it.Conns = it.Down+rows[i].Down, it.Up+rows[i].Up, it.Conns+1
	}
	out := make([]DeviceInfo, 0, len(e.devices))
	for _, d := range e.sortedDevices() {
		info := e.deviceInfo(d)
		t := byDev[d.DID]
		info.Down, info.Up, info.Conns = t.Down, t.Up, t.Conns
		if l := live[d.DID]; l != nil {
			info.LiveDown, info.LiveUp, info.LiveConns = l.Down, l.Up, l.Conns
		}
		if info.Spark = sparks[d.DID]; info.Spark == nil {
			info.Spark = make([]float64, n)
		}
		out = append(out, info)
	}
	return out, nil
}

// DeviceDetail is everything the device page shows.
type DeviceDetail struct {
	Device DeviceInfo `json:"device"`
	*Overview
	Zones map[string][2]uint64 `json:"zones"` // zone -> [down, up] bytes
}

// Device builds the device page data.
func (e *Engine) Device(did int64, rangeName string) (*DeviceDetail, error) {
	e.mu.Lock()
	d := e.devByDID[did]
	var info DeviceInfo
	if d != nil {
		info = e.deviceInfo(d)
	}
	e.mu.Unlock()
	if d == nil {
		return nil, fmt.Errorf("unknown device %d", did)
	}
	o, err := e.Overview(rangeName, "all", did)
	if err != nil {
		return nil, err
	}
	o.TopDevices = nil
	info.Down, info.Up, info.Conns = o.Down, o.Up, o.Conns
	det := &DeviceDetail{Device: info, Overview: o, Zones: map[string][2]uint64{}}
	r := ParseRange(rangeName)
	for _, z := range []flow.Zone{flow.ZoneWAN, flow.ZoneSite, flow.ZoneLocal} {
		f := r.filter(time.Now(), []uint8{uint8(z)})
		f.Dev = did
		if t, err := e.st.Totals(f); err == nil {
			det.Zones[z.String()] = [2]uint64{t.Down, t.Up}
		}
	}
	e.mu.Lock()
	rows, _ := e.liveRowsLocked(time.Now())
	e.mu.Unlock()
	for i := range rows {
		if rows[i].Dev == did && rows[i].Zone != "local" {
			det.Device.LiveDown += rows[i].Down
			det.Device.LiveUp += rows[i].Up
			det.Device.LiveConns++
		}
	}
	return det, nil
}

// SankeyNode is a node of the flow map.
type SankeyNode struct {
	ID    string `json:"id"`
	Col   int    `json:"col"` // 0 device, 1 service, 2 destination
	Ref   int64  `json:"ref"` // dictionary id, 0 for "other"
	Label string `json:"label"`
	Kind  string `json:"kind,omitempty"`
}

// SankeyLink is a weighted edge between two nodes.
type SankeyLink struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Down   uint64 `json:"down"`
	Up     uint64 `json:"up"`
}

// SankeyData is the flow map: devices to services to destinations.
type SankeyData struct {
	Nodes []SankeyNode `json:"nodes"`
	Links []SankeyLink `json:"links"`
	Total uint64       `json:"total"`
}

// Sankey builds the flow map for a range, keeping the heaviest nodes in each
// column and folding the rest into "Other".
func (e *Engine) Sankey(rangeName, zoneName string, dev int64) (*SankeyData, error) {
	r := ParseRange(rangeName)
	f := r.filter(time.Now(), ParseZones(zoneName))
	f.Dev = dev
	rows, err := e.st.Sankey(f, 5000)
	if err != nil {
		return nil, err
	}
	keep := func(pick func(store.SankeyRow) int64, n int) map[int64]bool {
		sum := map[int64]uint64{}
		for _, r := range rows {
			sum[pick(r)] += r.Down + r.Up
		}
		ids := make([]int64, 0, len(sum))
		for id := range sum {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool {
			if sum[ids[i]] != sum[ids[j]] {
				return sum[ids[i]] > sum[ids[j]]
			}
			return ids[i] < ids[j]
		})
		out := map[int64]bool{}
		for i, id := range ids {
			if i < n {
				out[id] = true
			}
		}
		return out
	}
	devs := keep(func(r store.SankeyRow) int64 { return r.Dev }, 10)
	svcs := keep(func(r store.SankeyRow) int64 { return r.Svc }, 8)
	dests := keep(func(r store.SankeyRow) int64 { return r.Dest }, 14)

	e.mu.Lock()
	defer e.mu.Unlock()
	data := &SankeyData{}
	nodes := map[string]bool{}
	node := func(col int, id int64, kept bool) string {
		n := SankeyNode{Col: col, Ref: id}
		prefix := [3]string{"d", "s", "t"}[col]
		if !kept {
			n.ID, n.Ref = prefix+"0", 0
			n.Label = [3]string{"Other devices", "Other services", "Other destinations"}[col]
			n.Kind = "other"
		} else {
			n.ID = fmt.Sprintf("%s%d", prefix, id)
			switch col {
			case 0:
				n.Label = e.devName(id)
			case 1:
				if s := e.svcByID[id]; s != nil {
					n.Label = s.Label
					if s.Tunnel {
						n.Kind = "tunnel"
					}
				}
			case 2:
				n.Label, n.Kind = e.destLabel(id)
			}
		}
		if !nodes[n.ID] {
			nodes[n.ID] = true
			data.Nodes = append(data.Nodes, n)
		}
		return n.ID
	}
	type pair struct{ a, b string }
	links := map[pair]*SankeyLink{}
	var order []pair
	link := func(a, b string, down, up uint64) {
		k := pair{a, b}
		l := links[k]
		if l == nil {
			l = &SankeyLink{Source: a, Target: b}
			links[k] = l
			order = append(order, k)
		}
		l.Down, l.Up = l.Down+down, l.Up+up
	}
	for _, r := range rows {
		d, s, t := node(0, r.Dev, devs[r.Dev]), node(1, r.Svc, svcs[r.Svc]), node(2, r.Dest, dests[r.Dest])
		link(d, s, r.Down, r.Up)
		link(s, t, r.Down, r.Up)
		data.Total += r.Down + r.Up
	}
	for _, k := range order {
		data.Links = append(data.Links, *links[k])
	}
	if data.Nodes == nil {
		data.Nodes, data.Links = []SankeyNode{}, []SankeyLink{}
	}
	return data, nil
}

// FlowRow is a stored connection as shown in the history table.
type FlowRow struct {
	ID         int64  `json:"id"`
	First      int64  `json:"first"`
	Last       int64  `json:"last"`
	Dev        int64  `json:"dev"`
	DevName    string `json:"devName"`
	LocalIP    string `json:"localIp"`
	LocalPort  uint16 `json:"localPort"`
	RemoteIP   string `json:"remoteIp"`
	RemotePort uint16 `json:"remotePort"`
	Proto      string `json:"proto"`
	Remote     string `json:"remote"`
	Dest       int64  `json:"dest"`
	DestLabel  string `json:"destLabel"`
	DestKind   string `json:"destKind"`
	Service    string `json:"service"`
	Tunnel     bool   `json:"tunnel"`
	Zone       string `json:"zone"`
	Down       uint64 `json:"down"`
	Up         uint64 `json:"up"`
}

// Flows returns stored connections for the history table.
func (e *Engine) Flows(rangeName, zoneName string, f store.ConnFilter) ([]FlowRow, error) {
	r := ParseRange(rangeName)
	now := time.Now().Unix() + 1
	f.From, f.To, f.Zones = now-r.Seconds, now, ParseZones(zoneName)
	rows, err := e.st.Conns(f)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]FlowRow, 0, len(rows))
	for _, c := range rows {
		fr := FlowRow{ID: c.ID, First: c.FirstTS, Last: c.LastTS, Dev: c.Dev, DevName: e.devName(c.Dev), LocalIP: c.LocalIP,
			LocalPort: c.LocalPort, RemoteIP: c.RemoteIP, RemotePort: c.RemotePort, Proto: enrich.ProtoName(c.Proto),
			Remote: c.RemoteName, Dest: c.Dest, Zone: flow.Zone(c.Zone).String(), Down: c.Down, Up: c.Up}
		fr.DestLabel, fr.DestKind = e.destLabel(c.Dest)
		if s := e.svcByID[c.Svc]; s != nil {
			fr.Service, fr.Tunnel = s.Label, s.Tunnel
		}
		out = append(out, fr)
	}
	return out, nil
}

// InterfaceInfo is an interface with its current rate, for the status page.
type InterfaceInfo struct {
	Index   uint32  `json:"index"`
	Name    string  `json:"name"`
	Type    string  `json:"type"`
	WAN     bool    `json:"wan"`
	Running bool    `json:"running"`
	RxBps   float64 `json:"rxBps"`
	TxBps   float64 `json:"txBps"`
}

// Status is the engine's part of the status page.
type Status struct {
	Mode        string          `json:"mode"`
	Started     int64           `json:"started"`
	LastExport  int64           `json:"lastExport"`
	ExportAge   float64         `json:"exportAge"`
	APIEnabled  bool            `json:"apiEnabled"`
	APIUp       bool            `json:"apiUp"`
	APIError    string          `json:"apiError"`
	APISince    int64           `json:"apiSince"`
	Router      map[string]any  `json:"router"`
	Interfaces  []InterfaceInfo `json:"interfaces"`
	LocalNets   []string        `json:"localNets"`
	Sites       []flow.Site     `json:"sites"`
	RouterAddrs []string        `json:"routerAddrs"`
	WAN         []string        `json:"wan"`
	Coverage    Coverage        `json:"coverage"`
	Devices     int             `json:"devices"`
	OpenConns   int             `json:"openConns"`
	TrackedConn int             `json:"trackedConns"`
	Naming      map[string]int  `json:"naming"` // open internet connections by how their destination is named
	DNSNames    int             `json:"dnsNames"`
	ASNDatabase string          `json:"asnDatabase"`
	ASNBuilt    int64           `json:"asnBuilt"`
	Learning    bool            `json:"learning"`
}

// Status reports the engine state for the status page.
func (e *Engine) Status() Status {
	now := time.Now()
	local, sites, router := e.topo.Snapshot()
	size, _, _ := e.names.Stats()
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{Mode: e.modeLocked(now), Started: e.start.Unix(), ExportAge: -1, APIEnabled: e.opts.APIConfigured,
		APIUp: e.live.apiUp, APIError: e.live.apiErr, Coverage: e.coverageLocked(now), Devices: len(e.devices),
		OpenConns: len(e.conns), TrackedConn: e.live.connTotal, DNSNames: size, Sites: sites,
		Naming: map[string]int{"domain": 0, "org": 0, "ip": 0}, Learning: e.alerts.Learning(now),
		LocalNets: []string{}, RouterAddrs: []string{}, WAN: []string{}, Interfaces: []InterfaceInfo{}}
	if s.Sites == nil {
		s.Sites = []flow.Site{}
	}
	if !e.lastExport.IsZero() {
		s.LastExport, s.ExportAge = e.lastExport.Unix(), now.Sub(e.lastExport).Seconds()
	}
	if !e.live.apiSince.IsZero() {
		s.APISince = e.live.apiSince.Unix()
	}
	for _, p := range local {
		s.LocalNets = append(s.LocalNets, p.String())
	}
	for _, a := range router {
		s.RouterAddrs = append(s.RouterAddrs, a.String())
	}
	for name := range e.live.wan {
		s.WAN = append(s.WAN, name)
	}
	sort.Strings(s.WAN)
	if i := e.live.info; e.live.apiUp || i.Identity != "" {
		s.Router = map[string]any{"identity": i.Identity, "version": i.Version, "board": i.Board, "uptime": i.Uptime, "cpuLoad": i.CPULoad}
	}
	for _, st := range e.live.ifaces {
		s.Interfaces = append(s.Interfaces, InterfaceInfo{Index: st.Index, Name: st.Name, Type: st.Type, WAN: e.live.wan[st.Name],
			Running: st.Running, RxBps: st.rxBps, TxBps: st.txBps})
	}
	sort.Slice(s.Interfaces, func(i, j int) bool { return s.Interfaces[i].Index < s.Interfaces[j].Index })
	for _, c := range e.conns {
		if c.zone == flow.ZoneWAN {
			s.Naming[c.dest.Kind]++
		}
	}
	if kind, built := e.asn.Describe(); kind != "" {
		s.ASNDatabase, s.ASNBuilt = kind, int64(built)
	}
	return s
}

// Today summarises internet traffic since a point in time (the wall display
// passes local midnight).
type Today struct {
	Since    int64   `json:"since"`
	Down     uint64  `json:"down"` // bytes
	Up       uint64  `json:"up"`
	Conns    uint64  `json:"conns"`
	PeakDown float64 `json:"peakDown"` // bits per second, one-minute average
	PeakUp   float64 `json:"peakUp"`
}

// Today returns totals and peaks for internet traffic since the given time.
func (e *Engine) Today(since time.Time) (*Today, error) {
	now := time.Now()
	if since.After(now) || now.Sub(since) > 48*time.Hour {
		since = now.Add(-24 * time.Hour)
	}
	f := store.Filter{From: since.Unix(), To: now.Unix() + 1, Zones: []uint8{uint8(flow.ZoneWAN)}}
	tot, err := e.st.Totals(f)
	if err != nil {
		return nil, err
	}
	t := &Today{Since: since.Unix(), Down: tot.Down, Up: tot.Up, Conns: tot.Conns}
	// Peaks come from the interface counters when the router API supplies
	// them, otherwise from flow records.
	pts, err := e.st.WanSeries(f.From, f.To, 60)
	if err != nil || len(pts) == 0 {
		if pts, err = e.st.Series(f, 60); err != nil {
			return nil, err
		}
	}
	for _, p := range pts {
		t.PeakDown = max(t.PeakDown, float64(p.Down)*8/60)
		t.PeakUp = max(t.PeakUp, float64(p.Up)*8/60)
	}
	return t, nil
}
