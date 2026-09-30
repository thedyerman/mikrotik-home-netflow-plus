package engine

import "time"

// ringLen is how many seconds of per-second history are kept in memory.
const ringLen = 1200

// secRing holds per-second byte and packet counts for the last ringLen seconds.
type secRing struct {
	ts                     [ringLen]int64
	down, up, downPk, upPk [ringLen]float64
}

// slot returns the index for a second, clearing whatever older second used it.
func (r *secRing) slot(sec int64) int {
	i := int(sec % ringLen)
	if r.ts[i] != sec {
		r.ts[i] = sec
		r.down[i], r.up[i], r.downPk[i], r.upPk[i] = 0, 0, 0, 0
	}
	return i
}

// at returns the index for a second if the ring still holds it.
func (r *secRing) at(sec int64) (int, bool) {
	i := int(sec % ringLen)
	return i, r.ts[i] == sec
}

// spread walks the step-second buckets overlapped by [start, end] and reports,
// for each, the cumulative fraction of the interval covered so far. A zero
// length interval falls entirely into the bucket containing it.
func spread(start, end time.Time, step int64, fn func(bucket int64, cum float64)) {
	s, e := start.UnixMilli(), end.UnixMilli()
	stepMs := step * 1000
	if e <= s {
		fn(e/stepMs*step, 1)
		return
	}
	dur := float64(e - s)
	for b := s / stepMs * stepMs; b < e; b += stepMs {
		fn(b/1000, float64(min(b+stepMs, e)-s)/dur)
	}
}

// splitter hands out an integer total in parts that always add up exactly.
type splitter struct{ total, given uint64 }

func (s *splitter) take(cum float64) uint64 {
	target := uint64(float64(s.total)*cum + 0.5)
	if cum >= 1 || target > s.total {
		target = s.total
	}
	part := target - s.given
	if target < s.given {
		part = 0
	} else {
		s.given = target
	}
	return part
}
