// Package pipeline fans the records out to the targets: one worker per target with its own
// bounded RAM queue, its batcher (by size or by time), its retries (exponential backoff with
// jitter; a permanent error drops the batch) and its status. Delivery is at-least-once and in
// order per target: a batch leaves the queue only after the store took it.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/selection"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/target"
)

// Status is one target's state for the UI.
type Status struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Enabled bool   `json:"enabled"`
	// Running is false when the target could not be built (a config error, in LastError).
	Running bool `json:"running"`
	// Connected is true after a successful send and false after a failed one; nil-ish "unknown"
	// before the first send is State "idle".
	Connected bool `json:"connected"`
	// State is idle (nothing sent yet), ok, retrying, error (config) or disabled.
	State       string    `json:"state"`
	LastSuccess time.Time `json:"last_success"`
	LastError   string    `json:"last_error"`
	LastErrorAt time.Time `json:"last_error_at"`
	Queued      int       `json:"queued"`
	InFlight    int       `json:"in_flight"`
	// Spilled is how many records wait on disk, SpillBytes their size.
	Spilled      int       `json:"spilled"`
	SpillBytes   int64     `json:"spill_bytes"`
	Sent         int64     `json:"sent"`
	Batches      int64     `json:"batches"`
	Dropped      int64     `json:"dropped"`
	Overflow     int64     `json:"overflow"`
	Retried      int64     `json:"retried"`
	RenderErrors int64     `json:"render_errors"`
	LastRender   string    `json:"last_render_error"`
	LagMS        int64     `json:"lag_ms"`
	NextRetry    time.Time `json:"next_retry"`
	Attempt      int       `json:"attempt"`
}

// Manager runs the workers.
type Manager struct {
	mu      sync.Mutex
	workers map[string]*worker
	log     func(format string, args ...any)
	// Now is the clock (a test replaces it).
	Now func() time.Time
	// Build makes the target of a configuration (a test replaces it).
	Build func(config.Target) (target.Target, error)
	// OnChange is called when a status changed in a way worth pushing (a send, an error).
	OnChange func()
	// SpillDir is where the targets' overflow goes (one directory per target id); "" disables
	// the spill.
	SpillDir string
}

// New makes a manager.
func New(log func(format string, args ...any)) *Manager {
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Manager{workers: map[string]*worker{}, log: log, Now: time.Now, Build: target.New}
}

// Apply reconciles the workers with the configuration: a new or changed target gets a (new)
// worker, keeping its queue when only the settings changed; a removed or disabled one stops.
func (m *Manager) Apply(targets []config.Target, selections []selection.Selection) {
	sels := map[string]*selection.Matcher{}
	for _, s := range selections {
		sels[s.ID] = selection.New(s)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, t := range targets {
		seen[t.ID] = true
		key := fingerprint(t)
		w := m.workers[t.ID]
		sel := sels[t.Selection]
		if sel == nil {
			sel = selection.New(selection.Selection{})
		}
		if w != nil && w.key == key {
			w.setSelection(sel)
			w.setMeta(t)
			continue
		}
		var q *queue
		if w != nil {
			w.stop()
			q = w.q
			q.resize(t.Queue.MaxRecords, m.spillDir(t.ID), int64(t.Queue.SpillMB)<<20)
		} else {
			q = newQueue(t.Queue.MaxRecords, m.spillDir(t.ID), int64(t.Queue.SpillMB)<<20)
		}
		nw := &worker{m: m, cfg: t, key: key, q: q, sel: sel, status: Status{ID: t.ID, Name: t.Name, Type: t.Type, Enabled: t.Enabled}}
		if w != nil {
			nw.status = w.snapshot()
			nw.status.Name, nw.status.Type, nw.status.Enabled = t.Name, t.Type, t.Enabled
			nw.status.Attempt, nw.status.NextRetry = 0, time.Time{}
		}
		m.workers[t.ID] = nw
		if !t.Enabled {
			nw.status.State = "disabled"
			nw.status.Running = false
			q.clear()
			continue
		}
		tgt, err := m.Build(t)
		if err != nil {
			nw.status.State = "error"
			nw.status.Running = false
			nw.status.LastError = err.Error()
			nw.status.LastErrorAt = m.Now()
			m.log("target %s: %v", t.Name, err)
			continue
		}
		nw.tgt = tgt
		nw.status.Running = true
		if nw.status.State == "" || nw.status.State == "disabled" || nw.status.State == "error" {
			nw.status.State = "idle"
			nw.status.LastError = ""
		}
		nw.start()
	}
	for id, w := range m.workers {
		if !seen[id] {
			w.stop()
			w.q.remove_()
			delete(m.workers, id)
		}
	}
}

func (m *Manager) spillDir(id string) string {
	if m.SpillDir == "" || id == "" {
		return ""
	}
	return filepath.Join(m.SpillDir, id)
}

// fingerprint is what a worker is built from: the target's configuration and secrets.
func fingerprint(t config.Target) string {
	b, _ := json.Marshal(t)
	s, _ := json.Marshal(t.Secrets)
	return string(b) + string(s)
}

// Dispatch queues a record at every enabled target whose selection matches.
func (m *Manager) Dispatch(r *record.Record) {
	m.mu.Lock()
	ws := make([]*worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.Unlock()
	for _, w := range ws {
		if !w.cfg.Enabled || w.tgt == nil {
			continue
		}
		if w.selection().Match(r.Subject()) {
			if n := w.q.push(r); n > 0 {
				w.overflow(n)
			}
		}
	}
}

// WantsMetric tells whether any enabled target's selection takes metrics of the group, so the
// poller reads only what is forwarded.
func (m *Manager) WantsMetric(group string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	subj := selection.Subject{Kind: record.KindMetric, MetricGroup: group}
	for _, w := range m.workers {
		if w.cfg.Enabled && w.tgt != nil && w.selection().Match(subj) {
			return true
		}
	}
	return false
}

// Matches tells which targets would take a record (for the UI's preview).
func (m *Manager) Matches(r *record.Record) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, w := range m.workers {
		if w.selection().Match(r.Subject()) {
			out = append(out, w.cfg.ID)
		}
	}
	sort.Strings(out)
	return out
}

// Status answers every target's status, sorted by name.
func (m *Manager) Status() []Status {
	m.mu.Lock()
	ws := make([]*worker, 0, len(m.workers))
	for _, w := range m.workers {
		ws = append(ws, w)
	}
	m.mu.Unlock()
	out := make([]Status, 0, len(ws))
	for _, w := range ws {
		out = append(out, w.snapshot())
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out
}

// Exposer answers the target of an id when it serves its values itself (Prometheus scrape).
func (m *Manager) Exposer(id string) (target.Exposer, bool) {
	m.mu.Lock()
	w := m.workers[id]
	m.mu.Unlock()
	if w == nil || w.tgt == nil || !w.cfg.Enabled {
		return nil, false
	}
	e, ok := w.tgt.(target.Exposer)
	return e, ok
}

// Clear drops a target's queue.
func (m *Manager) Clear(id string) bool {
	m.mu.Lock()
	w := m.workers[id]
	m.mu.Unlock()
	if w == nil {
		return false
	}
	n := w.q.clear()
	w.mu.Lock()
	w.status.Dropped += int64(n)
	w.mu.Unlock()
	w.kick()
	return true
}

// Stop ends every worker and writes what is still queued to the spill, so a restart goes on
// where it stopped (a target without a spill loses its queue).
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, w := range m.workers {
		w.stop()
		w.q.persist()
		delete(m.workers, id)
	}
}

func (m *Manager) changed() {
	if m.OnChange != nil {
		m.OnChange()
	}
}

// worker is one target's pipeline.
type worker struct {
	m      *Manager
	cfg    config.Target
	key    string
	q      *queue
	tgt    target.Target
	mu     sync.Mutex
	sel    *selection.Matcher
	status Status
	cancel context.CancelFunc
	done   chan struct{}
	wake   chan struct{}
}

func (w *worker) selection() *selection.Matcher {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.sel
}

func (w *worker) setSelection(s *selection.Matcher) {
	w.mu.Lock()
	w.sel = s
	w.mu.Unlock()
}

func (w *worker) setMeta(t config.Target) {
	w.mu.Lock()
	w.cfg.Name, w.cfg.Selection = t.Name, t.Selection
	w.status.Name = t.Name
	w.mu.Unlock()
}

func (w *worker) snapshot() Status {
	w.mu.Lock()
	s := w.status
	w.mu.Unlock()
	s.Queued, s.InFlight = w.q.len(), w.q.inflight()
	s.Spilled, s.SpillBytes = w.q.spilled()
	if oldest, ok := w.q.oldest(); ok {
		s.LagMS = w.m.Now().Sub(oldest).Milliseconds()
		if s.LagMS < 0 {
			s.LagMS = 0
		}
	}
	return s
}

func (w *worker) overflow(n int) {
	w.mu.Lock()
	w.status.Overflow += int64(n)
	w.status.Dropped += int64(n)
	w.mu.Unlock()
}

func (w *worker) start() {
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	w.wake = make(chan struct{}, 1)
	w.q.setWake(w.wake)
	go w.run(ctx)
}

func (w *worker) stop() {
	if w.cancel != nil {
		w.cancel()
		<-w.done
		w.cancel = nil
	}
}

// kick wakes the worker (a cleared queue, a new record).
func (w *worker) kick() {
	if w.wake != nil {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

func (w *worker) run(ctx context.Context) {
	defer close(w.done)
	interval := time.Duration(w.cfg.Batch.IntervalMS) * time.Millisecond
	for {
		batch := w.q.wait(ctx, w.cfg.Batch.Size, interval, w.wake)
		if ctx.Err() != nil {
			return
		}
		if len(batch) == 0 {
			continue
		}
		w.q.setInflight(len(batch))
		payloads, errs := w.tgt.Encode(batch)
		if len(errs) > 0 {
			w.mu.Lock()
			w.status.RenderErrors += int64(len(errs))
			w.status.LastRender = errs[len(errs)-1]
			w.status.Dropped += int64(len(errs))
			w.mu.Unlock()
		}
		ok := true
		for _, p := range payloads {
			if !w.deliver(ctx, p) {
				ok = false
				break
			}
		}
		if !ok {
			// the context ended during a retry: the batch stays queued for a restart
			w.q.setInflight(0)
			return
		}
		w.q.remove(len(batch))
		w.q.setInflight(0)
	}
}

// deliver sends one payload until the store takes it, a permanent error drops it, or the
// context ends (false).
func (w *worker) deliver(ctx context.Context, p target.Payload) bool {
	for attempt := 0; ; attempt++ {
		sctx, cancel := context.WithTimeout(ctx, time.Duration(w.cfg.TimeoutS)*time.Second+5*time.Second)
		err := w.tgt.Send(sctx, p)
		cancel()
		var partial *target.PartialError
		if errors.As(err, &partial) {
			w.mu.Lock()
			w.status.Dropped += int64(partial.Dropped)
			w.status.LastError = err.Error()
			w.status.LastErrorAt = w.m.Now()
			w.mu.Unlock()
			w.m.log("target %s: dropped %d records: %v", w.cfg.Name, partial.Dropped, err)
			p.Records -= partial.Dropped
			err = nil
		}
		if err == nil {
			w.mu.Lock()
			w.status.Sent += int64(p.Records)
			w.status.Batches++
			w.status.Connected = true
			w.status.State = "ok"
			w.status.LastSuccess = w.m.Now()
			w.status.Attempt = 0
			w.status.NextRetry = time.Time{}
			w.mu.Unlock()
			if attempt > 0 {
				w.m.changed()
			}
			return true
		}
		if ctx.Err() != nil {
			return false
		}
		w.mu.Lock()
		w.status.Connected = false
		w.status.LastError = err.Error()
		w.status.LastErrorAt = w.m.Now()
		if target.IsPermanent(err) {
			w.status.Dropped += int64(p.Records)
			w.status.State = "ok"
			w.status.Attempt = 0
			w.mu.Unlock()
			w.m.log("target %s: dropped %d records: %v", w.cfg.Name, p.Records, err)
			w.m.changed()
			return true
		}
		w.status.State = "retrying"
		w.status.Retried++
		w.status.Attempt = attempt + 1
		delay := w.backoff(attempt)
		w.status.NextRetry = w.m.Now().Add(delay)
		w.mu.Unlock()
		if attempt == 0 || attempt%10 == 9 {
			w.m.log("target %s: %v (retry %d in %s)", w.cfg.Name, err, attempt+1, delay.Round(time.Millisecond))
		}
		w.m.changed()
		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
		}
	}
}

// backoff is min * 2^attempt capped at max, with ±25 % jitter.
func (w *worker) backoff(attempt int) time.Duration {
	minD := time.Duration(w.cfg.Retry.MinMS) * time.Millisecond
	maxD := time.Duration(w.cfg.Retry.MaxMS) * time.Millisecond
	d := minD
	for i := 0; i < attempt && d < maxD; i++ {
		d *= 2
	}
	if d > maxD {
		d = maxD
	}
	j := time.Duration(float64(d) * (rand.Float64()*0.5 - 0.25)) //nolint:gosec // jitter
	return d + j
}

// queue is a bounded FIFO of records in memory with an overflow on disk (the spill): a record
// that finds the memory full goes to the spill, and the memory is refilled from the spill's
// oldest segment as it drains, so the order holds. Without a spill the oldest record not in
// flight is dropped on overflow.
type queue struct {
	mu       sync.Mutex
	items    []*record.Record
	max      int
	inFlight int
	first    time.Time // when the oldest unsent record arrived, for the batch timer
	wake     chan struct{}
	sp       *spill
}

func newQueue(max int, dir string, spillBytes int64) *queue {
	return &queue{max: max, sp: openSpill(dir, spillBytes, segmentSize(max))}
}

// segmentSize is how many records a spill segment holds: half the memory at most, so a segment
// always fits once the memory drains.
func segmentSize(max int) int {
	n := max / 2
	if n > 500 {
		n = 500
	}
	if n < 10 {
		n = 10
	}
	return n
}

func (q *queue) setWake(ch chan struct{}) {
	q.mu.Lock()
	q.wake = ch
	q.mu.Unlock()
}

func (q *queue) resize(max int, dir string, spillBytes int64) {
	q.mu.Lock()
	q.max = max
	if q.sp.dir != dir || q.sp.max != spillBytes {
		if q.sp.dir == dir {
			q.sp.max = spillBytes
			q.sp.segSize = segmentSize(max)
		} else {
			q.sp.remove()
			q.sp = openSpill(dir, spillBytes, segmentSize(max))
		}
	}
	q.mu.Unlock()
}

// push adds a record; it answers how many older records had to be dropped for it.
func (q *queue) push(r *record.Record) (dropped int) {
	q.mu.Lock()
	if !q.sp.empty() || len(q.items) >= q.max {
		if q.sp.enabled() {
			dropped = q.sp.add(r)
			q.mu.Unlock()
			return dropped
		}
		if q.inFlight >= len(q.items) {
			q.mu.Unlock()
			return 1 // everything queued is in flight: the newcomer goes
		}
		copy(q.items[q.inFlight:], q.items[q.inFlight+1:])
		q.items = q.items[:len(q.items)-1]
		dropped = 1
	}
	if len(q.items) == q.inFlight {
		q.first = time.Now()
	}
	q.items = append(q.items, r)
	wake := q.wake
	q.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	return dropped
}

// refill moves the spill's oldest records into the memory while they fit. Under the lock.
func (q *queue) refill() {
	for !q.sp.empty() && len(q.items)+q.sp.oldestSize() <= q.max {
		recs := q.sp.take()
		if len(recs) == 0 {
			continue
		}
		if len(q.items) == q.inFlight {
			q.first = time.Now()
		}
		q.items = append(q.items, recs...)
	}
}

// spilled answers how many records wait on disk (or for their segment) and the disk bytes.
func (q *queue) spilled() (int, int64) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.sp.records(), q.sp.bytes
}

// persist writes the memory's records to the spill ahead of what is there, for a restart.
func (q *queue) persist() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.sp.enabled() {
		return
	}
	q.sp.prepend(q.items)
	q.items = nil
	q.sp.flush()
}

// remove_ deletes the spill directory (the target is gone).
func (q *queue) remove_() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sp.remove()
}

func (q *queue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.items)
}

func (q *queue) inflight() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.inFlight
}

func (q *queue) setInflight(n int) {
	q.mu.Lock()
	q.inFlight = n
	if n == 0 && len(q.items) > 0 {
		q.first = time.Now()
	}
	q.mu.Unlock()
}

func (q *queue) oldest() (time.Time, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) == 0 {
		return time.Time{}, false
	}
	return q.items[0].TS, true
}

func (q *queue) clear() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := len(q.items) - q.inFlight
	q.items = q.items[:q.inFlight]
	n += q.sp.clear()
	return n
}

func (q *queue) remove(n int) {
	q.mu.Lock()
	if n > len(q.items) {
		n = len(q.items)
	}
	q.items = append([]*record.Record(nil), q.items[n:]...)
	q.refill()
	q.mu.Unlock()
}

// wait blocks until the queue holds size records, or interval passed since the oldest arrived,
// or ctx ends; it answers up to size records without removing them.
func (q *queue) wait(ctx context.Context, size int, interval time.Duration, wake <-chan struct{}) []*record.Record {
	for {
		q.mu.Lock()
		q.refill()
		n := len(q.items)
		first := q.first
		q.mu.Unlock()
		var timer <-chan time.Time
		if n >= size {
			return q.peek(size)
		}
		if n > 0 {
			d := interval - time.Since(first)
			if d <= 0 {
				return q.peek(size)
			}
			timer = time.After(d)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-timer:
		}
	}
}

func (q *queue) peek(n int) []*record.Record {
	q.mu.Lock()
	defer q.mu.Unlock()
	if n > len(q.items) {
		n = len(q.items)
	}
	out := make([]*record.Record, n)
	copy(out, q.items[:n])
	return out
}
