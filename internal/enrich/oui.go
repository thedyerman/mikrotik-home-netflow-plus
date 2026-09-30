package enrich

import (
	"bufio"
	"bytes"
	"compress/gzip"
	_ "embed"
	"strconv"
	"strings"
	"sync"
)

//go:embed data/oui.tsv.gz
var ouiGz []byte

var (
	ouiOnce sync.Once
	ouiMap  map[string]string // 6, 7 or 9 upper-case hex digits -> vendor
)

func loadOUI() {
	ouiMap = make(map[string]string, 60000)
	zr, err := gzip.NewReader(bytes.NewReader(ouiGz))
	if err != nil {
		return
	}
	sc := bufio.NewScanner(zr)
	for sc.Scan() {
		if prefix, vendor, ok := strings.Cut(sc.Text(), "\t"); ok {
			ouiMap[prefix] = vendor
		}
	}
}

// Vendor returns the manufacturer for a MAC address in aa:bb:cc:dd:ee:ff form.
// Locally administered (randomised) addresses have no manufacturer.
func Vendor(mac string) string {
	hex := strings.ToUpper(strings.ReplaceAll(mac, ":", ""))
	if len(hex) != 12 {
		return ""
	}
	first, err := strconv.ParseUint(hex[:2], 16, 8)
	if err != nil {
		return ""
	}
	if first&0x02 != 0 {
		return "Private address"
	}
	ouiOnce.Do(loadOUI)
	for _, n := range []int{9, 7, 6} { // most specific registry block first
		if v, ok := ouiMap[hex[:n]]; ok {
			return v
		}
	}
	return ""
}
