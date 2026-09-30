package flow

import (
	"fmt"
	"net/netip"
	"sync"
	"time"

	"mikrotik-home-netflow-plus/internal/ipfix"
)

const uptimeWrap = time.Duration(1<<32) * time.Millisecond

// Normalizer converts decoded IPFIX messages into flows.
type Normalizer struct {
	topo          *Topology
	activeTimeout time.Duration
	flowPort      uint16

	mu     sync.Mutex
	clocks map[netip.Addr]*exporterClock
}

// exporterClock tracks the offset between an exporter's clock and ours.
// The export time in the IPFIX header is truncated to the second, so
// receive-export lies in [offset, offset+1s); the minimum over a window is
// the offset.
type exporterClock struct {
	curMin, prevMin time.Duration
	windowStart     time.Time
	sysInitMs       uint64
	reboots         int
}

// ClockInfo describes an exporter's clock as seen by the collector.
type ClockInfo struct {
	Exporter string    `json:"exporter"`
	OffsetMs int64     `json:"offsetMs"` // collector clock minus router clock
	BootTime time.Time `json:"bootTime"`
	Reboots  int       `json:"reboots"` // reboots observed since the collector started
}

// NewNormalizer creates a normalizer. activeTimeout is the router's
// active-flow-timeout; flowPort is the UDP port the collector listens on, used
// to drop the router's own export stream.
func NewNormalizer(topo *Topology, activeTimeout time.Duration, flowPort uint16) *Normalizer {
	return &Normalizer{topo: topo, activeTimeout: activeTimeout, flowPort: flowPort, clocks: map[netip.Addr]*exporterClock{}}
}

// Normalize converts one message. rebooted is true when the exporter's boot
// time changed since the previous message.
func (n *Normalizer) Normalize(exporter netip.Addr, recv time.Time, msg *ipfix.Message) (flows []Flow, rebooted bool) {
	n.topo.LearnRouter(exporter)
	offset := n.offset(exporter, recv, msg.ExportTime)
	flows = make([]Flow, 0, len(msg.Records))
	for i := range msg.Records {
		r := &msg.Records[i]
		if r.SysInitMs != 0 && n.noteBoot(exporter, r.SysInitMs) {
			rebooted = true
		}
		if r.Proto == 17 && r.Src == exporter && r.DstPort == n.flowPort {
			continue // the export stream itself
		}
		f, ok := n.classify(r)
		if !ok {
			continue
		}
		f.Start, f.End = n.interval(r, recv, offset)
		f.Proto, f.Bytes, f.Packets, f.InIf, f.OutIf = r.Proto, r.Bytes, r.Packets, r.InIf, r.OutIf
		flows = append(flows, f)
	}
	return flows, rebooted
}

// classify applies the NAT inside view and zone rules measured against
// RouterOS 7.20: upload records carry the LAN source plus postNATSource (the
// WAN address); download records are addressed to the WAN address and carry
// the LAN host in postNATDestination.
func (n *Normalizer) classify(r *ipfix.Record) (Flow, bool) {
	var f Flow
	natDst := r.NatDst.IsValid() && !r.NatDst.IsUnspecified() && r.NatDst != r.Dst
	natSrc := r.NatSrc.IsValid() && !r.NatSrc.IsUnspecified() && r.NatSrc != r.Src
	switch {
	case natDst:
		f.Dir, f.Zone = Down, ZoneWAN
		f.LocalIP, f.LocalPort = r.NatDst, r.NatDstPort
		f.RemoteIP, f.RemotePort = r.Src, r.SrcPort
		f.PublicIP, f.PublicPort = r.Dst, r.DstPort
		n.topo.LearnRouter(r.Dst)
	case natSrc:
		f.Dir, f.Zone = Up, ZoneWAN
		f.LocalIP, f.LocalPort = r.Src, r.SrcPort
		f.RemoteIP, f.RemotePort = r.Dst, r.DstPort
		f.PublicIP, f.PublicPort = r.NatSrc, r.NatSrcPort
		n.topo.LearnRouter(r.NatSrc)
	default:
		if !r.Src.IsValid() || !r.Dst.IsValid() {
			return f, false
		}
		// Interface 0 on the way out means the packet was delivered to the
		// router itself (or was a broadcast it received). Interface 0 on the
		// way in is NOT a reliable sign of router-originated traffic: RouterOS
		// also reports it for some ICMP and reply packets of forwarded
		// connections. So only the outbound side is used as a hint, and an
		// address is accepted as the router's own only when it is seen on both
		// sides.
		n.topo.ObserveLocalDelivery(r.Src, r.InIf == 0, r.Dst, r.OutIf == 0)
		srcRouter, dstRouter := false, r.OutIf == 0
		side := n.topo.Classify(r.Src, r.Dst, srcRouter, dstRouter)
		if side.Drop {
			return f, false
		}
		f.Zone, f.Site, f.Router = side.Zone, side.Site, side.Router
		if side.LocalIsSrc {
			f.Dir = Up
			f.LocalIP, f.LocalPort, f.RemoteIP, f.RemotePort = r.Src, r.SrcPort, r.Dst, r.DstPort
		} else {
			f.Dir = Down
			f.LocalIP, f.LocalPort, f.RemoteIP, f.RemotePort = r.Dst, r.DstPort, r.Src, r.SrcPort
		}
	}
	if r.Proto == 1 || r.Proto == 58 { // ICMP has no ports; keep the key stable across directions
		f.LocalPort, f.RemotePort = 0, 0
	}
	// The source MAC is the LAN device only on records that entered from it.
	if f.Dir == Up && !f.Router && r.InIf != 0 && r.SrcMAC != [6]byte{} && r.SrcMAC[0]&1 == 0 {
		f.MAC = FormatMAC(r.SrcMAC)
	}
	return f, true
}

// interval returns the span of collector time the record's counters cover.
// RouterOS stamps flowEnd about one inactive timeout before it exports, but a
// record that hit the active timeout still counts every packet up to the
// export moment, so its bytes are spread up to the receive time.
func (n *Normalizer) interval(r *ipfix.Record, recv time.Time, offset time.Duration) (time.Time, time.Time) {
	boot := time.UnixMilli(int64(r.SysInitMs)).Add(offset)
	wall := func(uptimeMs uint32) time.Time {
		t := boot.Add(time.Duration(uptimeMs) * time.Millisecond)
		for recv.Sub(t) > uptimeWrap/2 { // the 32-bit uptime counter wraps every 49.7 days
			t = t.Add(uptimeWrap)
		}
		return t
	}
	start, end := wall(r.StartUptimeMs), wall(r.EndUptimeMs)
	dur := time.Duration(r.EndUptimeMs-r.StartUptimeMs) * time.Millisecond
	if dur >= n.activeTimeout-1500*time.Millisecond {
		end = recv
	}
	if end.After(recv) {
		end = recv
	}
	if start.After(end) {
		start = end
	}
	if recv.Sub(start) > 2*time.Hour { // nonsense timestamps: fall back to the receive time
		start = recv.Add(-dur)
		end = recv
	}
	return start, end
}

func (n *Normalizer) offset(exporter netip.Addr, recv time.Time, exportTime uint32) time.Duration {
	n.mu.Lock()
	defer n.mu.Unlock()
	d := recv.Sub(time.Unix(int64(exportTime), 0))
	c := n.clocks[exporter]
	if c == nil {
		c = &exporterClock{curMin: d, prevMin: d, windowStart: recv}
		n.clocks[exporter] = c
	}
	if recv.Sub(c.windowStart) > 2*time.Minute {
		c.prevMin, c.curMin, c.windowStart = c.curMin, d, recv
	}
	if d < c.curMin {
		c.curMin = d
	}
	return min(c.curMin, c.prevMin)
}

func (n *Normalizer) noteBoot(exporter netip.Addr, sysInitMs uint64) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	c := n.clocks[exporter]
	if c == nil {
		return false
	}
	prev := c.sysInitMs
	c.sysInitMs = sysInitMs
	if prev == 0 {
		return false
	}
	diff := int64(sysInitMs) - int64(prev)
	if diff < 0 {
		diff = -diff
	}
	if diff > 10_000 { // clock adjustments move the boot time slightly; a reboot moves it a lot
		c.reboots++
		return true
	}
	return false
}

// Clocks reports the clock state of every exporter seen.
func (n *Normalizer) Clocks() []ClockInfo {
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]ClockInfo, 0, len(n.clocks))
	for a, c := range n.clocks {
		off := min(c.curMin, c.prevMin)
		out = append(out, ClockInfo{
			Exporter: a.String(),
			OffsetMs: off.Milliseconds(),
			BootTime: time.UnixMilli(int64(c.sysInitMs)).Add(off),
			Reboots:  c.reboots,
		})
	}
	return out
}

// FormatMAC renders a MAC address in lower-case colon form.
func FormatMAC(m [6]byte) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", m[0], m[1], m[2], m[3], m[4], m[5])
}
