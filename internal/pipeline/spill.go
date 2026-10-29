package pipeline

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

// spill is a target's overflow on disk: segment files of records as JSON lines, written whole
// (one write per segment, nothing in normal operation), oldest first, capped in bytes - when the
// cap is reached the oldest segment goes. The records a segment will hold wait in `pending`
// until it is full; at a stop they are written too, so a restart finds the queue's tail.
type spill struct {
	dir      string
	max      int64 // bytes; 0: no spill
	segSize  int   // records per segment
	segments []segment
	pending  []*record.Record
	bytes    int64
	seq      int64
}

type segment struct {
	path    string
	records int
	bytes   int64
}

const segmentExt = ".ndjson"

// openSpill scans a target's spill directory for segments left by an earlier run.
func openSpill(dir string, maxBytes int64, segSize int) *spill {
	s := &spill{dir: dir, max: maxBytes, segSize: segSize, seq: 1 << 40}
	if dir == "" || maxBytes <= 0 {
		return s
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), segmentExt) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		p := filepath.Join(dir, n)
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		cnt := bytes.Count(b, []byte{'\n'})
		if cnt == 0 {
			_ = os.Remove(p)
			continue
		}
		s.segments = append(s.segments, segment{path: p, records: cnt, bytes: int64(len(b))})
		s.bytes += int64(len(b))
		if seq, err := strconv.ParseInt(strings.TrimSuffix(n, segmentExt), 10, 64); err == nil && seq >= s.seq {
			s.seq = seq + 1
		}
	}
	return s
}

// enabled tells whether records can be spilled.
func (s *spill) enabled() bool { return s.dir != "" && s.max > 0 }

// empty tells whether nothing waits on disk or in pending.
func (s *spill) empty() bool { return len(s.segments) == 0 && len(s.pending) == 0 }

// records answers how many wait, on disk and pending.
func (s *spill) records() int {
	n := len(s.pending)
	for _, seg := range s.segments {
		n += seg.records
	}
	return n
}

// add takes a record; dropped says how many older records had to go for it.
func (s *spill) add(r *record.Record) (dropped int) {
	s.pending = append(s.pending, r)
	if len(s.pending) >= s.segSize {
		dropped = s.flush()
	}
	return dropped
}

// flush writes the pending records as a segment, dropping the oldest segments when the cap
// would be exceeded.
func (s *spill) flush() (dropped int) {
	if len(s.pending) == 0 {
		return 0
	}
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	n := 0
	for _, r := range s.pending {
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		w.Write(b)
		w.WriteByte('\n')
		n++
	}
	w.Flush()
	s.pending = s.pending[:0]
	if n == 0 {
		return 0
	}
	size := int64(buf.Len())
	if size > s.max {
		return n // a segment larger than the whole cap: nothing to do with it
	}
	for s.bytes+size > s.max && len(s.segments) > 0 {
		dropped += s.dropOldest()
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return dropped + n
	}
	p := filepath.Join(s.dir, fmt.Sprintf("%020d%s", s.seq, segmentExt))
	s.seq++
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		_ = os.Remove(tmp)
		return dropped + n
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return dropped + n
	}
	s.segments = append(s.segments, segment{path: p, records: n, bytes: size})
	s.bytes += size
	return dropped
}

// prepend writes records as a segment ahead of every other (a stop's memory queue).
func (s *spill) prepend(recs []*record.Record) {
	if len(recs) == 0 || !s.enabled() {
		return
	}
	seq := s.seq
	if len(s.segments) > 0 {
		base := filepath.Base(s.segments[0].path)
		if n, err := strconv.ParseInt(strings.TrimSuffix(base, segmentExt), 10, 64); err == nil {
			seq = n - 1
		}
	} else {
		s.seq++
	}
	var buf bytes.Buffer
	n := 0
	for _, r := range recs {
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		buf.Write(b)
		buf.WriteByte('\n')
		n++
	}
	if n == 0 {
		return
	}
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return
	}
	p := filepath.Join(s.dir, fmt.Sprintf("%020d%s", seq, segmentExt))
	if err := os.WriteFile(p+".tmp", buf.Bytes(), 0o644); err != nil {
		return
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		return
	}
	s.segments = append([]segment{{path: p, records: n, bytes: int64(buf.Len())}}, s.segments...)
	s.bytes += int64(buf.Len())
}

// dropOldest removes the oldest segment and answers its record count.
func (s *spill) dropOldest() int {
	if len(s.segments) == 0 {
		return 0
	}
	seg := s.segments[0]
	s.segments = s.segments[1:]
	s.bytes -= seg.bytes
	_ = os.Remove(seg.path)
	return seg.records
}

// oldestSize answers the record count of what take would answer, 0 when nothing waits.
func (s *spill) oldestSize() int {
	if len(s.segments) > 0 {
		return s.segments[0].records
	}
	return len(s.pending)
}

// take answers the oldest waiting records - the oldest segment's, read and removed, or the
// pending ones when no segment is left.
func (s *spill) take() []*record.Record {
	if len(s.segments) > 0 {
		seg := s.segments[0]
		s.segments = s.segments[1:]
		s.bytes -= seg.bytes
		b, err := os.ReadFile(seg.path)
		_ = os.Remove(seg.path)
		if err != nil {
			return nil
		}
		out := make([]*record.Record, 0, seg.records)
		for _, line := range bytes.Split(b, []byte{'\n'}) {
			if len(line) == 0 {
				continue
			}
			var r record.Record
			if err := json.Unmarshal(line, &r); err != nil {
				continue
			}
			normalise(&r)
			out = append(out, &r)
		}
		return out
	}
	out := s.pending
	s.pending = nil
	return out
}

// normalise restores what the JSON round trip changed: integers come back as float64 in Value.
func normalise(r *record.Record) {
	if f, ok := r.Value.(float64); ok && (r.Type == "INTEGER" || r.Type == "ENUM") && f == float64(int64(f)) {
		r.Value = int64(f)
	}
	if r.Rooms == nil {
		r.Rooms = []string{}
	}
	if r.Functions == nil {
		r.Functions = []string{}
	}
}

// clear removes everything.
func (s *spill) clear() int {
	n := s.records()
	for _, seg := range s.segments {
		_ = os.Remove(seg.path)
	}
	s.segments = nil
	s.bytes = 0
	s.pending = nil
	return n
}

// remove deletes the directory.
func (s *spill) remove() {
	s.clear()
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}
