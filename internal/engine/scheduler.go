package engine

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// interval is a half-open byte range [Start, End).
type interval struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

func (i interval) size() int64 { return i.End - i.Start }

// segment is one connection's claim on [cursor, end). Only the owning worker
// advances cursor, and only after the bytes are written; the scheduler may
// lower end to hand the upper half to another connection.
type segment struct {
	mu     sync.Mutex
	cursor int64
	end    int64

	// Throughput sample for slow-connection replacement, reset per request.
	started atomic.Int64 // unix nanoseconds
	bytes   atomic.Int64
	abort   atomic.Pointer[func()]
}

func (s *segment) remaining() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.end - s.cursor
}

// window returns the cursor and how many bytes, at most limit, the worker may
// write next.
func (s *segment) window(limit int64) (cursor, n int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursor, min(limit, s.end-s.cursor)
}

// advance records n written bytes and reports whether the segment is done.
func (s *segment) advance(n int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursor += n
	return s.cursor >= s.end
}

func (s *segment) resetSample(now time.Time) {
	s.started.Store(now.UnixNano())
	s.bytes.Store(0)
}

// scheduler hands out work. A write in flight is at most bufferSize bytes and
// a split always leaves at least minSplit bytes to the victim, so with
// bufferSize <= minSplit a steal can never cut through a write in progress.
type scheduler struct {
	mu       sync.Mutex
	pending  []interval
	active   map[*segment]struct{}
	minSplit int64
}

// newScheduler pre-splits the remaining intervals so that up to connections
// requests can start without stealing from each other.
func newScheduler(remaining []interval, minSplit int64, connections int) *scheduler {
	s := &scheduler{pending: append([]interval(nil), remaining...), active: make(map[*segment]struct{}), minSplit: minSplit}
	for len(s.pending) < connections {
		i := s.largestPending()
		if i < 0 || s.pending[i].size() < 2*minSplit {
			break
		}
		iv := s.pending[i]
		mid := iv.Start + iv.size()/2
		s.pending[i] = interval{iv.Start, mid}
		s.pending = append(s.pending, interval{mid, iv.End})
	}
	sort.Slice(s.pending, func(a, b int) bool { return s.pending[a].Start < s.pending[b].Start })
	return s
}

func (s *scheduler) largestPending() int {
	best := -1
	for i, iv := range s.pending {
		if best < 0 || iv.size() > s.pending[best].size() {
			best = i
		}
	}
	return best
}

// next returns the next segment to fetch: the lowest pending interval, or else
// the upper half of the active segment with the most bytes left. It returns
// nil when nothing is left to hand out.
func (s *scheduler) next() *segment {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) > 0 {
		iv := s.pending[0]
		s.pending = s.pending[1:]
		seg := &segment{cursor: iv.Start, end: iv.End}
		s.active[seg] = struct{}{}
		return seg
	}
	var victim *segment
	var most int64
	for seg := range s.active {
		if r := seg.remaining(); r > most {
			victim, most = seg, r
		}
	}
	if victim == nil || most < 2*s.minSplit {
		return nil
	}
	victim.mu.Lock()
	r := victim.end - victim.cursor
	if r < 2*s.minSplit {
		victim.mu.Unlock()
		return nil
	}
	mid := victim.cursor + r/2
	stolen := &segment{cursor: mid, end: victim.end}
	victim.end = mid
	victim.mu.Unlock()
	s.active[stolen] = struct{}{}
	return stolen
}

// release returns a segment's unfinished bytes to the pending list.
func (s *scheduler) release(seg *segment) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.active, seg)
	seg.mu.Lock()
	if seg.cursor < seg.end {
		s.pending = append(s.pending, interval{seg.cursor, seg.end})
		sort.Slice(s.pending, func(a, b int) bool { return s.pending[a].Start < s.pending[b].Start })
	}
	seg.mu.Unlock()
}

// remaining lists every byte range not yet written, sorted by offset. Bytes a
// segment has written are excluded only after its cursor advanced, which
// happens after WriteAt returned.
func (s *scheduler) remaining() []interval {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := append([]interval(nil), s.pending...)
	for seg := range s.active {
		seg.mu.Lock()
		if seg.cursor < seg.end {
			out = append(out, interval{seg.cursor, seg.end})
		}
		seg.mu.Unlock()
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Start < out[b].Start })
	return out
}

// segments returns the active segments for monitoring.
func (s *scheduler) segments() []*segment {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*segment, 0, len(s.active))
	for seg := range s.active {
		out = append(out, seg)
	}
	return out
}

func totalSize(intervals []interval) int64 {
	var n int64
	for _, iv := range intervals {
		n += iv.size()
	}
	return n
}
