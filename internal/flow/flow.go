// Package flow turns raw IPFIX records into normalised, direction-aware flows:
// timestamps anchored on the collector clock, NAT resolved to the inside host,
// and each flow assigned to a zone.
package flow

import (
	"net/netip"
	"time"
)

// Zone says what is on the far side of a flow from the local device.
type Zone uint8

const (
	ZoneWAN   Zone = iota // the internet
	ZoneSite              // a remote site reached over a tunnel
	ZoneLocal             // the router itself or another local host
)

func (z Zone) String() string {
	switch z {
	case ZoneWAN:
		return "wan"
	case ZoneSite:
		return "site"
	default:
		return "local"
	}
}

// ParseZone is the inverse of Zone.String. ok is false for unknown names.
func ParseZone(s string) (Zone, bool) {
	switch s {
	case "wan":
		return ZoneWAN, true
	case "site":
		return ZoneSite, true
	case "local":
		return ZoneLocal, true
	}
	return 0, false
}

// Dir is the direction of a unidirectional flow relative to the local device.
type Dir uint8

const (
	Up   Dir = iota // local -> remote
	Down            // remote -> local
)

// Flow is one unidirectional flow record in the inside view.
type Flow struct {
	Start      time.Time // start of the interval the byte count covers
	End        time.Time // end of that interval (collector clock)
	LocalIP    netip.Addr
	RemoteIP   netip.Addr
	LocalPort  uint16
	RemotePort uint16
	Proto      uint8
	Dir        Dir
	Zone       Zone
	Site       string // set when Zone == ZoneSite
	Router     bool   // the local side is the router itself
	Bytes      uint64
	Packets    uint64
	MAC        string     // MAC of the local device when the record carried it
	PublicIP   netip.Addr // WAN address the connection was translated to, if any
	PublicPort uint16
	InIf       uint32
	OutIf      uint32
}

// ConnKey identifies a bidirectional connection in the inside view.
type ConnKey struct {
	Proto      uint8
	LocalIP    netip.Addr
	LocalPort  uint16
	RemoteIP   netip.Addr
	RemotePort uint16
}

// Key returns the connection key of the flow.
func (f *Flow) Key() ConnKey {
	return ConnKey{f.Proto, f.LocalIP, f.LocalPort, f.RemoteIP, f.RemotePort}
}
