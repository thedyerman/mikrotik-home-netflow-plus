package enrich

import (
	"net/netip"
	"os"
	"testing"
	"time"
)

func TestServices(t *testing.T) {
	cases := []struct {
		proto         uint8
		local, remote uint16
		want          string
		tunnel        bool
	}{
		{6, 51000, 443, "HTTPS", false},
		{17, 51000, 443, "QUIC", false},
		{17, 4500, 4500, "IPsec VPN", true},
		{17, 13231, 13231, "WireGuard", true},
		{6, 8096, 40000, "Jellyfin", false}, // inbound: the local port is the service
		{6, 40000, 40001, "TCP other", false},
		{1, 0, 0, "ICMP", false},
		{50, 0, 0, "IPsec VPN", true},
	}
	for _, c := range cases {
		got := LookupService(c.proto, c.local, c.remote)
		if got.Label != c.want || got.Tunnel != c.tunnel {
			t.Errorf("proto %d %d->%d: got %+v, want %s tunnel=%v", c.proto, c.local, c.remote, got, c.want, c.tunnel)
		}
	}
}

func TestVendor(t *testing.T) {
	for mac, want := range map[string]string{
		"18:4a:53:00:00:01": "Apple",
		"d0:ea:11:00:00:01": "Routerboard.com",
		"ca:00:00:00:00:01": "Private address", // locally administered bit set
		"bogus":             "",
	} {
		if got := Vendor(mac); got != want {
			t.Errorf("Vendor(%s) = %q, want %q", mac, got, want)
		}
	}
}

// The name a client asked for is recovered by walking CNAMEs backwards from
// the A record, and remembered after the router's cache entry expires.
func TestNamesFollowCNAMEs(t *testing.T) {
	n := NewNames(100)
	now := time.Unix(1_700_000_000, 0)
	n.Observe([]DNSRecord{
		{"CNAME", "www.example.com", "example.cdn-provider.net."},
		{"CNAME", "example.cdn-provider.net", "e1234.edge.akamai.net."},
		{"A", "e1234.edge.akamai.net", "203.0.113.7"},
		{"A", "plain.example.org", "203.0.113.8"},
	}, now)
	n.Observe(nil, now.Add(time.Hour)) // cache emptied; the mapping must survive
	for ip, want := range map[string]string{"203.0.113.7": "www.example.com", "203.0.113.8": "plain.example.org"} {
		got, ok := n.Lookup(netip.MustParseAddr(ip))
		if !ok || got != want {
			t.Errorf("Lookup(%s) = %q, %v; want %q", ip, got, ok, want)
		}
	}
	if _, ok := n.Lookup(netip.MustParseAddr("203.0.113.9")); ok {
		t.Error("unexpected name for an address never seen")
	}
	if ips, _, _ := n.Dirty(); len(ips) != 2 {
		t.Errorf("%d dirty entries, want 2", len(ips))
	}
	if ips, _, _ := n.Dirty(); len(ips) != 0 {
		t.Errorf("entries stayed dirty after being collected")
	}
}

func TestRegisteredDomain(t *testing.T) {
	for in, want := range map[string]string{
		"rr4---sn-abc.googlevideo.com":         "googlevideo.com",
		"a.b.example.co.uk.":                   "example.co.uk",
		"pkg-containers.githubusercontent.com": "githubusercontent.com", // private suffixes do not split the group
		"bucket.s3.us-west-2.amazonaws.com":    "amazonaws.com",
		"nas.lan":                              "nas.lan",
		"localhost":                            "localhost",
	} {
		if got := RegisteredDomain(in); got != want {
			t.Errorf("RegisteredDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestASNLookup(t *testing.T) {
	path := "../../.devdata/asn.mmdb"
	if _, err := os.Stat(path); err != nil {
		t.Skip("no ASN database available")
	}
	db, err := OpenASN(path)
	if err != nil {
		t.Fatal(err)
	}
	for ip, want := range map[string]uint32{"8.8.8.8": 15169, "1.1.1.1": 13335} {
		org, ok := db.Lookup(netip.MustParseAddr(ip))
		t.Logf("%s -> AS%d %q", ip, org.Number, org.Name)
		if !ok || org.Number != want || org.Name == "" {
			t.Errorf("Lookup(%s) = %+v, %v; want AS%d", ip, org, ok, want)
		}
	}
	if _, ok := db.Lookup(netip.MustParseAddr("192.168.1.1")); ok {
		t.Error("private address resolved to an organisation")
	}
	var nilDB *ASN
	if _, ok := nilDB.Lookup(netip.MustParseAddr("8.8.8.8")); ok {
		t.Error("nil database returned a result")
	}
	kind, built := db.Describe()
	t.Logf("database %q built %d", kind, built)
}
