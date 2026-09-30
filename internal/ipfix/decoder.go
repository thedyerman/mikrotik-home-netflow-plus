// Package ipfix decodes IPFIX (NetFlow v10) messages as exported by RouterOS
// Traffic Flow. It is template driven, so it also copes with field sets other
// than the two RouterOS sends today (258 for IPv4, 259 for IPv6).
package ipfix

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
)

// Information element IDs used by the decoder.
const (
	ieOctetDelta     = 1
	iePacketDelta    = 2
	ieProtocol       = 4
	ieTCPFlags       = 6
	ieSrcPort        = 7
	ieSrcIPv4        = 8
	ieInIf           = 10
	ieDstPort        = 11
	ieDstIPv4        = 12
	ieOutIf          = 14
	ieFlowEndUptime  = 21
	ieFlowStartUp    = 22
	ieSrcIPv6        = 27
	ieDstIPv6        = 28
	ieSrcMAC         = 56
	ieIPVersion      = 60
	ieDstMAC         = 80
	ieSysInitMs      = 160
	ieICMPTypeV4     = 176
	ieICMPCodeV4     = 177
	ieICMPTypeV6     = 178
	ieICMPCodeV6     = 179
	ieNatSrcIPv4     = 225
	ieNatDstIPv4     = 226
	ieNatSrcPort     = 227
	ieNatDstPort     = 228
	setTemplate      = 2
	setOptionsTmpl   = 3
	minDataSet       = 256
	varLen           = 0xFFFF
	maxFields        = 256
	maxTemplates     = 64
	headerLen        = 16
	maxRecordsPerMsg = 4096
)

// Record is one decoded flow record. Fields the template did not carry stay zero.
type Record struct {
	TemplateID    uint16
	IPVersion     uint8
	Proto         uint8
	TCPFlags      uint8
	ICMPType      uint8
	ICMPCode      uint8
	SrcPort       uint16
	DstPort       uint16
	NatSrcPort    uint16
	NatDstPort    uint16
	InIf          uint32
	OutIf         uint32
	StartUptimeMs uint32
	EndUptimeMs   uint32
	Bytes         uint64
	Packets       uint64
	SysInitMs     uint64
	Src           netip.Addr
	Dst           netip.Addr
	NatSrc        netip.Addr
	NatDst        netip.Addr
	SrcMAC        [6]byte
	DstMAC        [6]byte
}

// Message is one decoded IPFIX message.
type Message struct {
	ExportTime uint32 // seconds since the Unix epoch, router clock
	Sequence   uint32
	DomainID   uint32
	Records    []Record
	// Skipped is set when the message carried data whose template has not
	// been seen yet, so its record count is unknown.
	Skipped bool
}

type field struct {
	ID         uint16 `json:"id"`
	Len        uint16 `json:"len"`
	Enterprise uint32 `json:"ent,omitempty"`
}

type tmplKey struct {
	exporter netip.Addr
	domain   uint32
	id       uint16
}

// Stats are cumulative decoder counters.
type Stats struct {
	Messages        uint64 `json:"messages"`
	Records         uint64 `json:"records"`
	TemplateSets    uint64 `json:"templateSets"`
	UnknownTemplate uint64 `json:"unknownTemplate"`
	Errors          uint64 `json:"errors"`
	LostRecords     uint64 `json:"lostRecords"`
	SequenceResets  uint64 `json:"sequenceResets"`
	Templates       int    `json:"templates"`
}

type seqKey struct {
	exporter netip.Addr
	domain   uint32
}

// Decoder holds the template cache and sequence state for all exporters.
type Decoder struct {
	mu        sync.Mutex
	templates map[tmplKey][]field
	nextSeq   map[seqKey]uint32
	stats     Stats
	path      string
	dirty     bool
}

// NewDecoder creates a decoder. If cachePath is set, templates are loaded from
// it and written back whenever they change, so a restart does not have to wait
// for the router to resend them.
func NewDecoder(cachePath string) *Decoder {
	d := &Decoder{templates: map[tmplKey][]field{}, nextSeq: map[seqKey]uint32{}, path: cachePath}
	d.load()
	return d
}

var errShort = errors.New("ipfix: truncated message")

// Decode parses one UDP payload received from exporter.
func (d *Decoder) Decode(exporter netip.Addr, b []byte) (*Message, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	msg, err := d.decode(exporter, b)
	if err != nil {
		d.stats.Errors++
		return nil, err
	}
	d.stats.Messages++
	d.stats.Records += uint64(len(msg.Records))
	d.trackSequence(exporter, msg)
	if d.dirty {
		d.save()
	}
	return msg, nil
}

func (d *Decoder) decode(exporter netip.Addr, b []byte) (*Message, error) {
	if len(b) < headerLen {
		return nil, errShort
	}
	if v := binary.BigEndian.Uint16(b[0:2]); v != 10 {
		return nil, fmt.Errorf("ipfix: unsupported version %d", v)
	}
	if n := int(binary.BigEndian.Uint16(b[2:4])); n < headerLen || n > len(b) {
		return nil, errShort
	} else {
		b = b[:n]
	}
	msg := &Message{
		ExportTime: binary.BigEndian.Uint32(b[4:8]),
		Sequence:   binary.BigEndian.Uint32(b[8:12]),
		DomainID:   binary.BigEndian.Uint32(b[12:16]),
	}
	off := headerLen
	for off+4 <= len(b) {
		setID := binary.BigEndian.Uint16(b[off : off+2])
		setLen := int(binary.BigEndian.Uint16(b[off+2 : off+4]))
		if setLen < 4 || off+setLen > len(b) {
			return nil, errShort
		}
		body := b[off+4 : off+setLen]
		off += setLen
		switch {
		case setID == setTemplate:
			d.stats.TemplateSets++
			if err := d.parseTemplates(exporter, msg.DomainID, body); err != nil {
				return nil, err
			}
		case setID == setOptionsTmpl:
			// Options data is not used.
		case setID >= minDataSet:
			tmpl, ok := d.templates[tmplKey{exporter, msg.DomainID, setID}]
			if !ok {
				d.stats.UnknownTemplate++
				msg.Skipped = true
				continue
			}
			for len(body) > 0 && len(msg.Records) < maxRecordsPerMsg {
				rec, n, ok := decodeRecord(tmpl, body)
				if !ok {
					break // set padding
				}
				rec.TemplateID = setID
				msg.Records = append(msg.Records, rec)
				body = body[n:]
			}
		}
	}
	return msg, nil
}

func (d *Decoder) parseTemplates(exporter netip.Addr, domain uint32, body []byte) error {
	for len(body) >= 4 {
		id := binary.BigEndian.Uint16(body[0:2])
		count := int(binary.BigEndian.Uint16(body[2:4]))
		body = body[4:]
		if id < minDataSet {
			return nil // padding
		}
		if count == 0 { // template withdrawal
			delete(d.templates, tmplKey{exporter, domain, id})
			d.dirty = true
			continue
		}
		if count > maxFields {
			return fmt.Errorf("ipfix: template %d has %d fields", id, count)
		}
		fields := make([]field, 0, count)
		fixed := 0
		for i := 0; i < count; i++ {
			if len(body) < 4 {
				return errShort
			}
			f := field{ID: binary.BigEndian.Uint16(body[0:2]), Len: binary.BigEndian.Uint16(body[2:4])}
			body = body[4:]
			if f.ID&0x8000 != 0 {
				if len(body) < 4 {
					return errShort
				}
				f.ID &= 0x7FFF
				f.Enterprise = binary.BigEndian.Uint32(body[0:4])
				body = body[4:]
			}
			if f.Len != varLen {
				fixed += int(f.Len)
			}
			fields = append(fields, f)
		}
		if fixed == 0 {
			return fmt.Errorf("ipfix: template %d has no fixed-length fields", id)
		}
		key := tmplKey{exporter, domain, id}
		if old, ok := d.templates[key]; !ok || !sameFields(old, fields) {
			if !ok && len(d.templates) >= maxTemplates {
				return errors.New("ipfix: too many templates")
			}
			d.templates[key] = fields
			d.dirty = true
		}
	}
	return nil
}

func sameFields(a, b []field) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func decodeRecord(tmpl []field, b []byte) (rec Record, n int, ok bool) {
	for _, f := range tmpl {
		l := int(f.Len)
		if f.Len == varLen {
			if len(b) < n+1 {
				return rec, 0, false
			}
			l = int(b[n])
			n++
			if l == 255 {
				if len(b) < n+2 {
					return rec, 0, false
				}
				l = int(binary.BigEndian.Uint16(b[n : n+2]))
				n += 2
			}
		}
		if len(b) < n+l {
			return rec, 0, false
		}
		v := b[n : n+l]
		n += l
		if f.Enterprise != 0 {
			continue
		}
		switch f.ID {
		case ieOctetDelta:
			rec.Bytes = uintN(v)
		case iePacketDelta:
			rec.Packets = uintN(v)
		case ieProtocol:
			rec.Proto = uint8(uintN(v))
		case ieTCPFlags:
			rec.TCPFlags = uint8(uintN(v))
		case ieSrcPort:
			rec.SrcPort = uint16(uintN(v))
		case ieDstPort:
			rec.DstPort = uint16(uintN(v))
		case ieInIf:
			rec.InIf = uint32(uintN(v))
		case ieOutIf:
			rec.OutIf = uint32(uintN(v))
		case ieFlowEndUptime:
			rec.EndUptimeMs = uint32(uintN(v))
		case ieFlowStartUp:
			rec.StartUptimeMs = uint32(uintN(v))
		case ieSysInitMs:
			rec.SysInitMs = uintN(v)
		case ieIPVersion:
			rec.IPVersion = uint8(uintN(v))
		case ieSrcIPv4, ieSrcIPv6:
			rec.Src = addr(v)
		case ieDstIPv4, ieDstIPv6:
			rec.Dst = addr(v)
		case ieNatSrcIPv4:
			rec.NatSrc = addr(v)
		case ieNatDstIPv4:
			rec.NatDst = addr(v)
		case ieNatSrcPort:
			rec.NatSrcPort = uint16(uintN(v))
		case ieNatDstPort:
			rec.NatDstPort = uint16(uintN(v))
		case ieSrcMAC:
			copy(rec.SrcMAC[:], v)
		case ieDstMAC:
			copy(rec.DstMAC[:], v)
		case ieICMPTypeV4, ieICMPTypeV6:
			rec.ICMPType = uint8(uintN(v))
		case ieICMPCodeV4, ieICMPCodeV6:
			rec.ICMPCode = uint8(uintN(v))
		}
	}
	if n == 0 {
		return rec, 0, false
	}
	return rec, n, true
}

func uintN(b []byte) uint64 {
	var v uint64
	if len(b) > 8 {
		b = b[len(b)-8:]
	}
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func addr(b []byte) netip.Addr {
	a, _ := netip.AddrFromSlice(b)
	return a
}

// trackSequence counts records lost in transit. The IPFIX sequence number is
// the running count of data records sent before this message.
func (d *Decoder) trackSequence(exporter netip.Addr, msg *Message) {
	k := seqKey{exporter, msg.DomainID}
	if msg.Skipped {
		// Records were skipped for want of a template (normal just after a
		// cold start), so the next expected sequence number is unknown. Resync
		// on the following message instead of reporting a false loss.
		delete(d.nextSeq, k)
		return
	}
	if want, ok := d.nextSeq[k]; ok && msg.Sequence != want {
		gap := msg.Sequence - want // wraps correctly in uint32
		if gap < 1<<20 {
			d.stats.LostRecords += uint64(gap)
		} else {
			d.stats.SequenceResets++
		}
	}
	d.nextSeq[k] = msg.Sequence + uint32(len(msg.Records))
}

// Stats returns a snapshot of the counters.
func (d *Decoder) Stats() Stats {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := d.stats
	s.Templates = len(d.templates)
	return s
}

type cachedTemplate struct {
	Exporter string  `json:"exporter"`
	Domain   uint32  `json:"domain"`
	ID       uint16  `json:"id"`
	Fields   []field `json:"fields"`
}

func (d *Decoder) load() {
	if d.path == "" {
		return
	}
	raw, err := os.ReadFile(d.path)
	if err != nil {
		return
	}
	var list []cachedTemplate
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	for _, t := range list {
		if a, err := netip.ParseAddr(t.Exporter); err == nil && len(t.Fields) > 0 {
			d.templates[tmplKey{a, t.Domain, t.ID}] = t.Fields
		}
	}
}

func (d *Decoder) save() {
	d.dirty = false
	if d.path == "" {
		return
	}
	list := make([]cachedTemplate, 0, len(d.templates))
	for k, f := range d.templates {
		list = append(list, cachedTemplate{k.exporter.String(), k.domain, k.id, f})
	}
	raw, err := json.Marshal(list)
	if err != nil {
		return
	}
	tmp := d.path + ".tmp"
	if os.MkdirAll(filepath.Dir(d.path), 0o755) == nil && os.WriteFile(tmp, raw, 0o644) == nil {
		_ = os.Rename(tmp, d.path)
	}
}
