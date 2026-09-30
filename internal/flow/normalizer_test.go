package flow_test

import (
	"encoding/json"
	"net/netip"
	"os"
	"testing"
	"time"

	"mikrotik-home-netflow-plus/internal/flow"
	"mikrotik-home-netflow-plus/internal/ipfix"
)

const (
	captureFile = "../../testdata/captures/routeros7-ipfix-sample.bin"
	truthFile   = "../../testdata/captures/routeros7-ipfix-sample.groundtruth.json"
)

type counters struct {
	T     float64 `json:"t"`
	WanRx uint64  `json:"wan_rx"`
	WanTx uint64  `json:"wan_tx"`
}

type truth struct {
	Enabled   counters `json:"enabled"`
	Disabled  counters `json:"disabled"`
	Transfers []struct {
		Label     string  `json:"label"`
		LocalPort uint16  `json:"local_port"`
		RemoteIP  string  `json:"remote_ip"`
		Down      uint64  `json:"down"`
		Up        uint64  `json:"up"`
		Start     float64 `json:"start"`
		End       float64 `json:"end"`
	} `json:"transfers"`
}

func testTopology() *flow.Topology {
	return flow.NewTopology(
		[]netip.Prefix{netip.MustParsePrefix("192.168.88.0/24"), netip.MustParsePrefix("fe80::/10")},
		[]flow.Site{{Name: "office", Prefix: netip.MustParsePrefix("192.168.99.0/24")}},
		nil)
}

// loadFlows replays the sample capture: real RouterOS 7 export with every
// address anonymised (see testdata/captures/README.md).
func loadFlows(t *testing.T) ([]flow.Flow, truth, ipfix.Stats) {
	t.Helper()
	dgrams, err := ipfix.ReadCapture(captureFile)
	if err != nil {
		t.Skipf("capture not available: %v", err)
	}
	var tr truth
	raw, err := os.ReadFile(truthFile)
	if err != nil {
		t.Skipf("ground truth not available: %v", err)
	}
	if err := json.Unmarshal(raw, &tr); err != nil {
		t.Fatal(err)
	}
	dec := ipfix.NewDecoder("")
	norm := flow.NewNormalizer(testTopology(), time.Minute, 2055)
	exporter := netip.MustParseAddr("192.168.88.1")
	var flows []flow.Flow
	for _, d := range dgrams {
		msg, err := dec.Decode(exporter, d.Payload)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		fl, rebooted := norm.Normalize(exporter, d.Received, msg)
		if rebooted {
			t.Fatal("unexpected reboot detected")
		}
		flows = append(flows, fl...)
	}
	return flows, tr, dec.Stats()
}

func TestDecodeCapture(t *testing.T) {
	_, _, st := loadFlows(t)
	if st.Messages != 431 || st.Records != 4289 {
		t.Errorf("messages=%d records=%d, want 431 and 4289", st.Messages, st.Records)
	}
	if st.UnknownTemplate != 0 || st.LostRecords != 0 || st.Errors != 0 {
		t.Errorf("unexpected decoder problems: %+v", st)
	}
	if st.Templates != 2 {
		t.Errorf("templates=%d, want 2 (IPv4 and IPv6)", st.Templates)
	}
}

// The three transfers were made from 192.168.88.234 with exact payload sizes.
// Flow records count IP bytes, so they exceed the payload by header overhead.
func TestKnownTransfers(t *testing.T) {
	flows, tr, _ := loadFlows(t)
	local := netip.MustParseAddr("192.168.88.234")
	for _, x := range tr.Transfers {
		var up, down uint64
		var first, last time.Time
		for _, f := range flows {
			if f.LocalIP != local || f.LocalPort != x.LocalPort || f.RemoteIP.String() != x.RemoteIP {
				continue
			}
			if f.Zone != flow.ZoneWAN || f.Router {
				t.Errorf("%s: zone=%v router=%v, want wan device traffic", x.Label, f.Zone, f.Router)
			}
			if f.PublicIP.String() != "100.64.1.2" {
				t.Errorf("%s: public address %v, want the WAN address", x.Label, f.PublicIP)
			}
			if f.Dir == flow.Up {
				up += f.Bytes
				if f.MAC != "" && f.MAC != "18:4a:53:6f:57:9f" {
					t.Errorf("%s: device MAC %q", x.Label, f.MAC)
				}
			} else {
				down += f.Bytes
			}
			if first.IsZero() || f.Start.Before(first) {
				first = f.Start
			}
			if f.End.After(last) {
				last = f.End
			}
		}
		payload, got := x.Down, down
		if x.Up > x.Down {
			payload, got = x.Up, up
		}
		ratio := float64(got) / float64(payload)
		if ratio < 1.02 || ratio > 1.06 {
			t.Errorf("%s: flow bytes %d vs payload %d (ratio %.3f), want 1.02..1.06", x.Label, got, payload, ratio)
		}
		// Timestamps are anchored on the collector clock although the router's
		// clock was about 6 s off: the flow must bracket the real transfer.
		start, end := time.UnixMilli(int64(x.Start*1000)), time.UnixMilli(int64(x.End*1000))
		if d := first.Sub(start); d < -2*time.Second || d > 3*time.Second {
			t.Errorf("%s: flow starts %v after the transfer started", x.Label, d)
		}
		if d := last.Sub(end); d < -2*time.Second || d > 20*time.Second {
			t.Errorf("%s: flow ends %v after the transfer ended", x.Label, d)
		}
	}
}

// Flow totals must agree with the WAN interface counters read from the router
// at the start and end of the capture.
func TestCoverageAgainstInterfaceCounters(t *testing.T) {
	flows, tr, _ := loadFlows(t)
	var down, up, downPk, upPk uint64
	for _, f := range flows {
		if f.Zone != flow.ZoneWAN {
			continue
		}
		if f.Dir == flow.Down {
			down, downPk = down+f.Bytes, downPk+f.Packets
		} else {
			up, upPk = up+f.Bytes, upPk+f.Packets
		}
	}
	const eth = 14 // interface counters include the Ethernet header
	check := func(name string, flowBytes, counter uint64) {
		cov := float64(flowBytes) / float64(counter)
		t.Logf("%s coverage %.1f%%", name, cov*100)
		if cov < 0.97 || cov > 1.01 {
			t.Errorf("%s coverage %.3f, want 0.97..1.01", name, cov)
		}
	}
	check("download", down+eth*downPk, tr.Disabled.WanRx-tr.Enabled.WanRx)
	check("upload", up+eth*upPk, tr.Disabled.WanTx-tr.Enabled.WanTx)
}

func TestZones(t *testing.T) {
	flows, _, _ := loadFlows(t)
	zones := map[flow.Zone]int{}
	var siteBytes, routerWAN uint64
	for _, f := range flows {
		zones[f.Zone]++
		if f.Zone == flow.ZoneSite {
			if f.Site != "office" {
				t.Fatalf("site name %q", f.Site)
			}
			siteBytes += f.Bytes
		}
		if f.Router && f.Zone == flow.ZoneWAN {
			routerWAN += f.Bytes
		}
		if f.Proto == 17 && f.RemotePort == 2055 && f.LocalPort == 2055 {
			t.Fatal("the router's own export stream was not dropped")
		}
		if f.Zone == flow.ZoneWAN && !f.Router && !f.LocalIP.Is4() {
			t.Fatalf("unexpected IPv6 LAN device %v", f.LocalIP)
		}
	}
	if zones[flow.ZoneWAN] == 0 || zones[flow.ZoneSite] == 0 || zones[flow.ZoneLocal] == 0 {
		t.Errorf("expected flows in every zone, got %v", zones)
	}
	if siteBytes == 0 {
		t.Error("no traffic attributed to the remote site")
	}
	// The WireGuard transport packets are the router's own internet traffic.
	if routerWAN < 100_000 {
		t.Errorf("router WAN traffic %d bytes, expected the WireGuard transport to be attributed to the router", routerWAN)
	}
}

// Router addresses are learned from the records themselves; nothing else may
// be mistaken for one. (Interface 0 on ingress also appears on some ICMP and
// reply packets from internet hosts, which must not be learned.)
func TestLearnedRouterAddresses(t *testing.T) {
	dgrams, err := ipfix.ReadCapture(captureFile)
	if err != nil {
		t.Skipf("capture not available: %v", err)
	}
	topo := testTopology()
	dec := ipfix.NewDecoder("")
	norm := flow.NewNormalizer(topo, time.Minute, 2055)
	exporter := netip.MustParseAddr("192.168.88.1")
	var flows []flow.Flow
	for _, d := range dgrams {
		msg, _ := dec.Decode(exporter, d.Payload)
		fl, _ := norm.Normalize(exporter, d.Received, msg)
		flows = append(flows, fl...)
	}
	_, _, router := topo.Snapshot()
	got := map[string]bool{}
	for _, a := range router {
		got[a.String()] = true
	}
	// LAN address, WAN (CGNAT) address, the global IPv6 address used as the
	// WireGuard endpoint, and the WAN link-local address.
	for _, want := range []string{"192.168.88.1", "100.64.1.2", "2001:db8:0:3::1", "fe80::1:1"} {
		if !got[want] {
			t.Errorf("router address %s was not learned", want)
		}
		delete(got, want)
	}
	// Anything left over is a mistake: an internet host seen with ingress
	// interface 0, or a broadcast address seen with egress interface 0.
	if len(got) > 0 {
		t.Errorf("unexpected router addresses learned: %v", got)
	}
	// 198.18.1.8 is an internet host whose ICMP replies arrive with ingress
	// interface 0; they are still internet downloads of the LAN device.
	for _, f := range flows {
		if f.RemoteIP.String() == "198.18.1.8" && !f.Router && f.Zone != flow.ZoneWAN {
			t.Fatalf("flow with 198.18.1.8 classified as zone %v", f.Zone)
		}
	}
}
