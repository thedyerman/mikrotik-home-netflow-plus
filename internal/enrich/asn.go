package enrich

import (
	"net/netip"
	"regexp"
	"strings"
	"sync"

	"github.com/oschwald/maxminddb-golang/v2"
)

// ASN looks up the organisation that announces an address, from an MMDB file
// in the common ASN layout (DB-IP ASN Lite, IPinfo Lite, GeoLite2 ASN).
type ASN struct {
	db    *maxminddb.Reader
	path  string
	mu    sync.Mutex
	cache map[netip.Addr]Org
}

// Org is the owner of an address.
type Org struct {
	Number uint32
	Name   string
}

type asnRecord struct {
	Number uint32 `maxminddb:"autonomous_system_number"`
	Name   string `maxminddb:"autonomous_system_organization"`
	// IPinfo Lite layout
	ASN    string `maxminddb:"asn"`
	ASName string `maxminddb:"as_name"`
}

// OpenASN opens the database at path. A nil *ASN is valid and finds nothing.
func OpenASN(path string) (*ASN, error) {
	db, err := maxminddb.Open(path)
	if err != nil {
		return nil, err
	}
	return &ASN{db: db, path: path, cache: map[netip.Addr]Org{}}, nil
}

// Path returns the file the database was loaded from.
func (a *ASN) Path() string {
	if a == nil {
		return ""
	}
	return a.path
}

// Describe returns the database type and build time for the status page.
func (a *ASN) Describe() (kind string, buildEpoch uint) {
	if a == nil {
		return "", 0
	}
	return a.db.Metadata.DatabaseType, a.db.Metadata.BuildEpoch
}

var orgSuffix = regexp.MustCompile(`(?i)[,.]?\s+(inc|llc|ltd|limited|corp|corporation|co|company|gmbh|ag|s\.?a|b\.?v|ab|plc|pty|l\.?p)\.?$`)

// Lookup returns the organisation for an address.
func (a *ASN) Lookup(ip netip.Addr) (Org, bool) {
	if a == nil {
		return Org{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if o, ok := a.cache[ip]; ok {
		return o, o.Name != ""
	}
	var rec asnRecord
	_ = a.db.Lookup(ip).Decode(&rec)
	o := Org{Number: rec.Number, Name: rec.Name}
	if o.Name == "" && rec.ASName != "" {
		o.Name = rec.ASName
		for _, c := range strings.TrimPrefix(rec.ASN, "AS") {
			if c < '0' || c > '9' {
				break
			}
			o.Number = o.Number*10 + uint32(c-'0')
		}
	}
	o.Name = cleanOrg(o.Name)
	if len(a.cache) > 50000 {
		a.cache = map[netip.Addr]Org{}
	}
	a.cache[ip] = o
	return o, o.Name != ""
}

func cleanOrg(s string) string {
	s = strings.TrimSpace(s)
	for i := 0; i < 2; i++ {
		short := strings.TrimRight(orgSuffix.ReplaceAllString(s, ""), " ,.")
		if short == s || len(short) < 3 {
			break
		}
		s = short
	}
	return s
}
