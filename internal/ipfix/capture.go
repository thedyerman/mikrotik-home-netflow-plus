package ipfix

import (
	"encoding/binary"
	"io"
	"math"
	"os"
	"time"
)

// Datagram is one captured UDP payload with the time it was received.
type Datagram struct {
	Received time.Time
	Payload  []byte
}

// ReadCapture reads a capture file: repeated records of a little-endian
// float64 Unix receive time, a little-endian uint32 length and the payload.
func ReadCapture(path string) ([]Datagram, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Datagram
	hdr := make([]byte, 12)
	for {
		if _, err := io.ReadFull(f, hdr); err != nil {
			if err == io.EOF {
				return out, nil
			}
			return nil, err
		}
		ts := math.Float64frombits(binary.LittleEndian.Uint64(hdr[0:8]))
		payload := make([]byte, binary.LittleEndian.Uint32(hdr[8:12]))
		if _, err := io.ReadFull(f, payload); err != nil {
			return nil, err
		}
		sec, frac := math.Modf(ts)
		out = append(out, Datagram{time.Unix(int64(sec), int64(frac*1e9)), payload})
	}
}
