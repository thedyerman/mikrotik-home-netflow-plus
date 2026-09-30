package routeros

import (
	"context"
	"errors"
	"log/slog"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"mikrotik-home-netflow-plus/internal/enrich"
)

// Interface is one router interface with its byte counters.
type Interface struct {
	Index   uint32 // equals the interface number used in flow records
	Name    string
	Type    string
	MAC     string
	RxBytes uint64
	TxBytes uint64
	Running bool
	Slave   bool
}

// Conn is one entry of the router's connection table.
type Conn struct {
	Proto        uint8
	Src, Dst     netip.Addr // original direction: Src is the initiator
	ReplySrc     netip.Addr
	ReplyDst     netip.Addr
	SrcPort      uint16
	DstPort      uint16
	ReplySrcPort uint16
	ReplyDstPort uint16
	OrigBytes    uint64
	ReplBytes    uint64
	OrigRate     uint64 // bits per second, initiator to responder
	ReplRate     uint64 // bits per second, responder to initiator
	SrcNAT       bool
	DstNAT       bool
	FastTrack    bool
	TCPState     string
}

// Neighbor ties an address to a MAC, with whatever name the router knows.
type Neighbor struct {
	IP       netip.Addr
	MAC      string // lower case
	Hostname string
	Comment  string
}

// Address is an address configured on the router.
type Address struct {
	Prefix    netip.Prefix
	Interface string
}

// Route is an active route.
type Route struct {
	Dst       netip.Prefix
	Interface string
}

// Info is slow-changing router state.
type Info struct {
	Identity       string
	Version        string
	Board          string
	Uptime         string
	CPULoad        int
	Addresses      []Address
	Routes         []Route
	WANInterfaces  []string
	WireGuardPorts []uint16
}

// Sink receives everything the poller reads.
type Sink interface {
	Interfaces(at time.Time, ifs []Interface)
	Connections(at time.Time, active []Conn, total int)
	DNSCache(at time.Time, recs []enrich.DNSRecord)
	Neighbors(at time.Time, n []Neighbor)
	RouterInfo(at time.Time, info Info)
	APIState(up bool, errText string)
}

// Intervals set how often each kind of data is read.
type Intervals struct {
	Interfaces  time.Duration
	Connections time.Duration
	Count       time.Duration
	DNS         time.Duration
	Neighbors   time.Duration
	Info        time.Duration
}

// DefaultIntervals are tuned against an L009: the active-connection query
// costs about 50 ms of router time, the interface query about 10 ms.
func DefaultIntervals() Intervals {
	return Intervals{
		Interfaces:  time.Second,
		Connections: 2 * time.Second,
		Count:       10 * time.Second,
		DNS:         10 * time.Second,
		Neighbors:   60 * time.Second,
		Info:        60 * time.Second,
	}
}

// Poller keeps an API session open and feeds a Sink.
type Poller struct {
	opts Options
	iv   Intervals
	sink Sink
	log  *slog.Logger
}

// NewPoller creates a poller.
func NewPoller(opts Options, iv Intervals, sink Sink, log *slog.Logger) *Poller {
	return &Poller{opts: opts, iv: iv, sink: sink, log: log}
}

// Run polls until ctx is cancelled, reconnecting with backoff on errors.
func (p *Poller) Run(ctx context.Context) {
	backoff := 2 * time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := p.session(ctx)
		if ctx.Err() != nil {
			return
		}
		p.sink.APIState(false, errString(err))
		p.log.Warn("router API session ended", "err", err)
		if time.Since(start) > time.Minute {
			backoff = 2 * time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, time.Minute)
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (p *Poller) session(ctx context.Context) error {
	c, err := Dial(ctx, p.opts)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	type task struct {
		name  string
		every time.Duration
		next  time.Time
		run   func(time.Time) error
		trap  string // last error reply from the router for this task
	}
	total := -1
	tasks := []*task{
		// Interfaces first: their types are needed to interpret addresses and routes.
		{name: "interfaces", every: p.iv.Interfaces, run: func(t time.Time) error { return p.pollInterfaces(c, t) }},
		{name: "router info", every: p.iv.Info, run: func(t time.Time) error { return p.pollInfo(c, t) }},
		{name: "leases and ARP", every: p.iv.Neighbors, run: func(t time.Time) error { return p.pollNeighbors(c, t) }},
		{name: "DNS cache", every: p.iv.DNS, run: func(t time.Time) error { return p.pollDNS(c, t) }},
		{name: "connection count", every: p.iv.Count, run: func(time.Time) error {
			n, err := countConns(c)
			if err == nil {
				total = n
			}
			return err
		}},
		{name: "connections", every: p.iv.Connections, run: func(t time.Time) error {
			conns, err := activeConns(c)
			if err == nil {
				p.sink.Connections(t, conns, total)
			}
			return err
		}},
	}
	p.sink.APIState(true, "")
	p.log.Info("router API connected", "addr", p.opts.Addr, "tls", p.opts.TLS)
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	reported := ""
	for {
		now := time.Now()
		for _, t := range tasks {
			if now.Before(t.next) {
				continue
			}
			t.next = now.Add(t.every)
			err := t.run(now)
			var trap *TrapError
			switch {
			case err == nil:
				t.trap = ""
			case errors.As(err, &trap):
				// An error reply leaves the session usable. Some are transient
				// (a connection vanished while the table was being read);
				// others, like missing permissions, persist and are surfaced.
				t.trap = t.name + ": " + trap.Message
				p.log.Debug("router API command failed", "task", t.name, "err", trap.Message)
			default:
				return err
			}
		}
		current := ""
		for _, t := range tasks {
			if t.trap != "" {
				current = t.trap
				break
			}
		}
		if current != reported {
			reported = current
			p.sink.APIState(true, current)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func (p *Poller) pollInterfaces(c *Client, at time.Time) error {
	rows, _, err := c.Run("/interface/print", "=.proplist=.id,name,type,mac-address,rx-byte,tx-byte,running,slave")
	if err != nil {
		return err
	}
	ifs := make([]Interface, 0, len(rows))
	for _, r := range rows {
		idx, _ := strconv.ParseUint(strings.TrimPrefix(r[".id"], "*"), 16, 32)
		ifs = append(ifs, Interface{
			Index: uint32(idx), Name: r["name"], Type: r["type"], MAC: strings.ToLower(r["mac-address"]),
			RxBytes: u64(r["rx-byte"]), TxBytes: u64(r["tx-byte"]), Running: r["running"] == "true", Slave: r["slave"] == "true",
		})
	}
	p.sink.Interfaces(at, ifs)
	return nil
}

const connProps = "=.proplist=protocol,src-address,src-port,dst-address,dst-port,reply-src-address,reply-src-port," +
	"reply-dst-address,reply-dst-port,orig-bytes,repl-bytes,orig-rate,repl-rate,tcp-state,fasttrack,srcnat,dstnat"

// activeConns returns connections that are moving data right now. Asking only
// for those keeps the query cheap on the router.
func activeConns(c *Client) ([]Conn, error) {
	var out []Conn
	for _, menu := range []string{"/ip/firewall/connection/print", "/ipv6/firewall/connection/print"} {
		rows, _, err := c.Run(menu, connProps, "?>orig-rate=0", "?>repl-rate=0", "?#|")
		if err != nil {
			if _, trap := err.(*TrapError); trap && strings.HasPrefix(menu, "/ipv6") {
				continue // IPv6 package or firewall not present
			}
			return nil, err
		}
		for _, r := range rows {
			cn := Conn{
				Proto: protoNumber(r["protocol"]),
				Src:   addr(r["src-address"]), Dst: addr(r["dst-address"]),
				ReplySrc: addr(r["reply-src-address"]), ReplyDst: addr(r["reply-dst-address"]),
				SrcPort: u16(r["src-port"]), DstPort: u16(r["dst-port"]),
				ReplySrcPort: u16(r["reply-src-port"]), ReplyDstPort: u16(r["reply-dst-port"]),
				OrigBytes: u64(r["orig-bytes"]), ReplBytes: u64(r["repl-bytes"]),
				OrigRate: u64(r["orig-rate"]), ReplRate: u64(r["repl-rate"]),
				SrcNAT: r["srcnat"] == "true", DstNAT: r["dstnat"] == "true", FastTrack: r["fasttrack"] == "true",
				TCPState: r["tcp-state"],
			}
			if cn.Src.IsValid() && cn.Dst.IsValid() {
				out = append(out, cn)
			}
		}
	}
	return out, nil
}

func countConns(c *Client) (int, error) {
	_, done, err := c.Run("/ip/firewall/connection/print", "=count-only=")
	if err != nil {
		return 0, err
	}
	n, _ := strconv.Atoi(done["ret"])
	return n, nil
}

func (p *Poller) pollDNS(c *Client, at time.Time) error {
	rows, _, err := c.Run("/ip/dns/cache/print", "=.proplist=type,name,data")
	if err != nil {
		return err
	}
	recs := make([]enrich.DNSRecord, 0, len(rows))
	for _, r := range rows {
		recs = append(recs, enrich.DNSRecord{Type: r["type"], Name: r["name"], Data: r["data"]})
	}
	p.sink.DNSCache(at, recs)
	return nil
}

func (p *Poller) pollNeighbors(c *Client, at time.Time) error {
	var out []Neighbor
	rows, _, err := c.Run("/ip/arp/print", "=.proplist=address,mac-address,complete")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if a := addr(r["address"]); a.IsValid() && r["mac-address"] != "" {
			out = append(out, Neighbor{IP: a, MAC: strings.ToLower(r["mac-address"])})
		}
	}
	// Leases come after ARP so their names win for the same address.
	rows, _, err = c.Run("/ip/dhcp-server/lease/print", "=.proplist=address,active-address,mac-address,active-mac-address,host-name,comment")
	if err != nil {
		return err
	}
	for _, r := range rows {
		ip, mac := firstNonEmpty(r["active-address"], r["address"]), firstNonEmpty(r["active-mac-address"], r["mac-address"])
		if a := addr(ip); a.IsValid() && mac != "" {
			out = append(out, Neighbor{IP: a, MAC: strings.ToLower(mac), Hostname: r["host-name"], Comment: r["comment"]})
		}
	}
	if rows, _, err := c.Run("/ipv6/neighbor/print", "=.proplist=address,mac-address"); err == nil {
		for _, r := range rows {
			if a := addr(r["address"]); a.IsValid() && r["mac-address"] != "" {
				out = append(out, Neighbor{IP: a, MAC: strings.ToLower(r["mac-address"])})
			}
		}
	}
	p.sink.Neighbors(at, out)
	return nil
}

func (p *Poller) pollInfo(c *Client, at time.Time) error {
	var info Info
	rows, _, err := c.Run("/system/resource/print", "=.proplist=uptime,cpu-load,version,board-name")
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		info.Uptime, info.Version, info.Board = rows[0]["uptime"], rows[0]["version"], rows[0]["board-name"]
		info.CPULoad, _ = strconv.Atoi(rows[0]["cpu-load"])
	}
	if rows, _, err := c.Run("/system/identity/print"); err == nil && len(rows) > 0 {
		info.Identity = rows[0]["name"]
	}
	wan := map[string]bool{}
	for _, menu := range []string{"/ip/address/print", "/ipv6/address/print"} {
		rows, _, err := c.Run(menu, "=.proplist=address,interface,disabled,invalid")
		if err != nil {
			continue
		}
		for _, r := range rows {
			if r["disabled"] == "true" || r["invalid"] == "true" {
				continue
			}
			if pfx, err := netip.ParsePrefix(r["address"]); err == nil {
				info.Addresses = append(info.Addresses, Address{pfx, r["interface"]})
			}
		}
	}
	for _, menu := range []string{"/ip/route/print", "/ipv6/route/print"} {
		rows, _, err := c.Run(menu, "=.proplist=dst-address,immediate-gw", "?active=true")
		if err != nil {
			continue
		}
		for _, r := range rows {
			pfx, err := netip.ParsePrefix(r["dst-address"])
			if err != nil {
				continue
			}
			gw := r["immediate-gw"]
			if i := strings.LastIndexByte(gw, '%'); i >= 0 {
				gw = gw[i+1:]
			}
			if gw == "" {
				continue
			}
			if pfx.Bits() == 0 {
				wan[gw] = true
			}
			info.Routes = append(info.Routes, Route{pfx.Masked(), gw})
		}
	}
	if rows, _, err := c.Run("/interface/list/member/print", "=.proplist=list,interface,disabled"); err == nil {
		for _, r := range rows {
			if strings.EqualFold(r["list"], "WAN") && r["disabled"] != "true" {
				wan[r["interface"]] = true
			}
		}
	}
	for name := range wan {
		info.WANInterfaces = append(info.WANInterfaces, name)
	}
	if rows, _, err := c.Run("/interface/wireguard/print", "=.proplist=listen-port"); err == nil {
		for _, r := range rows {
			if port := u16(r["listen-port"]); port != 0 {
				info.WireGuardPorts = append(info.WireGuardPorts, port)
			}
		}
	}
	p.sink.RouterInfo(at, info)
	return nil
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func addr(s string) netip.Addr {
	a, _ := netip.ParseAddr(s)
	return a.Unmap()
}

func u64(s string) uint64 {
	n, _ := strconv.ParseUint(s, 10, 64)
	return n
}

func u16(s string) uint16 {
	n, _ := strconv.ParseUint(s, 10, 16)
	return uint16(n)
}

func protoNumber(s string) uint8 {
	switch s {
	case "tcp":
		return 6
	case "udp":
		return 17
	case "icmp":
		return 1
	case "icmpv6":
		return 58
	case "gre":
		return 47
	case "ipsec-esp":
		return 50
	case "ipsec-ah":
		return 51
	case "igmp":
		return 2
	case "ospf":
		return 89
	case "ipencap", "ipip":
		return 4
	}
	n, _ := strconv.ParseUint(s, 10, 8)
	return uint8(n)
}
