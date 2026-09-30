package engine

import (
	"io"
	"log/slog"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"mikrotik-home-netflow-plus/internal/alert"
	"mikrotik-home-netflow-plus/internal/enrich"
	"mikrotik-home-netflow-plus/internal/flow"
	"mikrotik-home-netflow-plus/internal/ipfix"
	"mikrotik-home-netflow-plus/internal/store"
)

const captureFile = "../../testdata/captures/routeros7-ipfix-sample.bin"

// replayCapture runs the sample capture through a fresh engine and store.
func replayCapture(t *testing.T) (*Engine, *store.Store, []flow.Flow) {
	t.Helper()
	dgrams, err := ipfix.ReadCapture(captureFile)
	if err != nil {
		t.Skipf("capture not available: %v", err)
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	topo := flow.NewTopology(
		[]netip.Prefix{netip.MustParsePrefix("192.168.88.0/24"), netip.MustParsePrefix("fe80::/10")},
		[]flow.Site{{Name: "office", Prefix: netip.MustParsePrefix("192.168.99.0/24")}}, nil)
	eng, err := New(Options{FoldBelow: 10_000}, st, topo, enrich.NewNames(1000), nil, alert.NewManager(st, "test", log), log)
	if err != nil {
		t.Fatal(err)
	}
	dec := ipfix.NewDecoder("")
	norm := flow.NewNormalizer(topo, time.Minute, 2055)
	exporter := netip.MustParseAddr("192.168.88.1")
	var all []flow.Flow
	for _, d := range dgrams {
		msg, err := dec.Decode(exporter, d.Payload)
		if err != nil {
			t.Fatal(err)
		}
		flows, _ := norm.Normalize(exporter, d.Received, msg)
		eng.AddFlows(d.Received, flows)
		all = append(all, flows...)
	}
	eng.Flush(dgrams[len(dgrams)-1].Received)
	return eng, st, all
}

// Every byte of every flow must end up in the rollups, no more and no less.
func TestRollupsConserveBytes(t *testing.T) {
	_, st, flows := replayCapture(t)
	for _, zone := range []flow.Zone{flow.ZoneWAN, flow.ZoneSite, flow.ZoneLocal} {
		var down, up uint64
		for _, f := range flows {
			if f.Zone != zone {
				continue
			}
			if f.Dir == flow.Down {
				down += f.Bytes
			} else {
				up += f.Bytes
			}
		}
		for _, hourly := range []bool{false, true} {
			got, err := st.Totals(store.Filter{From: 0, To: 1 << 40, Zones: []uint8{uint8(zone)}, Hourly: hourly})
			if err != nil {
				t.Fatal(err)
			}
			if got.Down != down || got.Up != up {
				t.Errorf("zone %v hourly=%v: rollup down=%d up=%d, flows down=%d up=%d", zone, hourly, got.Down, got.Up, down, up)
			}
		}
	}
}

func TestDevicesAndConnections(t *testing.T) {
	eng, st, _ := replayCapture(t)
	eng.mu.Lock()
	dev := eng.devices["18:4a:53:6f:57:9f"]
	var ipOnly int
	for id, d := range eng.devices {
		if len(id) > 3 && id[:3] == "ip-" && eng.topo.IsLocal(d.IPs[0]) && d.IPs[0].Is4() {
			ipOnly++
		}
	}
	routerDID := eng.router.DID
	eng.mu.Unlock()
	if dev == nil {
		t.Fatal("the test machine was not identified by its MAC address")
	}
	if dev.Vendor != "Apple" {
		t.Errorf("vendor %q, want Apple", dev.Vendor)
	}
	if len(dev.IPs) == 0 || dev.IPs[0].String() != "192.168.88.234" {
		t.Errorf("device addresses %v", dev.IPs)
	}
	if ipOnly > 2 {
		t.Errorf("%d IPv4 LAN devices were left without a MAC", ipOnly)
	}

	// The three test transfers are well above the fold threshold and must be stored.
	rows, err := st.Conns(store.ConnFilter{From: 0, To: 1 << 40, Dev: dev.DID, Text: "198.18.1.55", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	found := map[uint16]store.ConnRow{}
	for _, r := range rows {
		found[r.LocalPort] = r
	}
	for port, want := range map[uint16][2]uint64{55767: {50_000_000, 0}, 55768: {90_000_000, 0}, 55787: {0, 15_000_000}} {
		r, ok := found[port]
		if !ok {
			t.Errorf("connection from local port %d was not stored", port)
			continue
		}
		if r.Down < want[0] || r.Up < want[1] || float64(r.Down+r.Up) > float64(want[0]+want[1])*1.08 {
			t.Errorf("port %d: stored down=%d up=%d, expected payload down=%d up=%d plus headers", port, r.Down, r.Up, want[0], want[1])
		}
	}

	// Small connections are folded into rollups, not stored one by one.
	all, _ := st.Conns(store.ConnFilter{From: 0, To: 1 << 40, Limit: 1000})
	for _, r := range all {
		if r.Up+r.Down < 10_000 {
			t.Fatalf("connection %d with %d bytes should have been folded", r.ID, r.Up+r.Down)
		}
	}
	if len(all) < 20 || len(all) > 400 {
		t.Errorf("%d connections stored; expected roughly a tenth of the ~1800 seen", len(all))
	}

	// The router's own traffic (WireGuard transport, DNS upstream) is its own device.
	tot, _ := st.Totals(store.Filter{From: 0, To: 1 << 40, Dev: routerDID, Zones: []uint8{uint8(flow.ZoneWAN)}})
	if tot.Down+tot.Up < 100_000 {
		t.Errorf("router device has %d WAN bytes", tot.Down+tot.Up)
	}
}

// A long transfer must be spread over the minutes it actually ran in.
func TestSpreadAcrossMinutes(t *testing.T) {
	var got []uint64
	var buckets []int64
	s := splitter{total: 1000}
	start := time.Unix(999_999_960+30, 0) // 30 s into a minute
	spread(start, start.Add(90*time.Second), 60, func(b int64, cum float64) {
		buckets = append(buckets, b)
		got = append(got, s.take(cum))
	})
	if len(got) != 2 || got[0]+got[1] != 1000 {
		t.Fatalf("parts %v over buckets %v", got, buckets)
	}
	if got[0] < 330 || got[0] > 336 {
		t.Errorf("first minute got %d of 1000, want about a third", got[0])
	}
	// Zero-length interval: everything in one bucket.
	n := 0
	spread(start, start, 60, func(_ int64, cum float64) {
		n++
		if cum != 1 {
			t.Errorf("cum=%v", cum)
		}
	})
	if n != 1 {
		t.Errorf("zero-length interval produced %d buckets", n)
	}
}
