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
	"mikrotik-home-netflow-plus/internal/routeros"
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

// Displayed rates rise quickly, fall slowly, and a connection that leaves the
// router's active list fades out instead of vanishing.
func TestLiveRateSmoothing(t *testing.T) {
	eng, _, _ := replayCapture(t)
	eng.mu.Lock()
	eng.live.wan = map[string]bool{"ether1": true}
	eng.live.apiUp = true
	eng.mu.Unlock()

	// WAN counters: 1 s polls at a steady 80 Mb/s, then silence.
	t0 := time.Unix(1_800_000_000, 0)
	poll := func(sec int, rx uint64) {
		eng.Interfaces(t0.Add(time.Duration(sec)*time.Second), []routeros.Interface{{Index: 2, Name: "ether1", Type: "ether", RxBytes: rx, Running: true}})
	}
	poll(0, 0)
	for s := 1; s <= 10; s++ {
		poll(s, uint64(s)*10_000_000) // 10 MB/s = 80 Mb/s
	}
	eng.mu.Lock()
	rising := eng.live.wanDownS
	eng.mu.Unlock()
	if rising < 0.95*80e6 || rising > 80e6 {
		t.Errorf("after 10 s at 80 Mb/s the smoothed rate is %.1f Mb/s", rising/1e6)
	}
	poll(11, 100_000_000)
	poll(12, 100_000_000)
	eng.mu.Lock()
	falling := eng.live.wanDownS
	eng.mu.Unlock()
	if falling < 0.6*80e6 || falling > 0.8*80e6 { // release constant 6 s: about 72% left after 2 s
		t.Errorf("2 s after traffic stopped the smoothed rate is %.1f Mb/s, want 48..64", falling/1e6)
	}

	// A connection: one snapshot at 10 Mb/s, then it disappears.
	conn := routeros.Conn{Proto: 6, Src: netip.MustParseAddr("192.168.88.234"), SrcPort: 50000,
		Dst: netip.MustParseAddr("198.18.1.55"), DstPort: 443, ReplySrc: netip.MustParseAddr("198.18.1.55"), ReplySrcPort: 443,
		ReplyDst: netip.MustParseAddr("100.64.1.2"), ReplyDstPort: 50000, OrigRate: 500_000, ReplRate: 10_000_000, SrcNAT: true}
	eng.Connections(t0, []routeros.Conn{conn}, 1)
	rows, mode, _ := eng.LiveRows(10)
	if mode != "api" || len(rows) != 1 || rows[0].Down != 10e6 {
		t.Fatalf("first sight: mode=%s rows=%d down=%v (want shown at full rate immediately)", mode, len(rows), rows)
	}
	eng.Connections(t0.Add(2*time.Second), nil, 0)
	rows, _, _ = eng.LiveRows(10)
	if len(rows) != 1 || rows[0].Down > 0.8*10e6 || rows[0].Down < 0.6*10e6 {
		t.Fatalf("2 s after vanishing the connection should be fading, got %v", rows)
	}
	eng.Connections(t0.Add(40*time.Second), nil, 0)
	if rows, _, _ = eng.LiveRows(10); len(rows) != 0 {
		t.Fatalf("after 40 s the connection should be gone, got %v", rows)
	}
}
