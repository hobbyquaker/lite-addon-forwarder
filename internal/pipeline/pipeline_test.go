package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/selection"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/target"
)

type fake struct {
	mu     sync.Mutex
	sent   [][]string
	fail   int // sends to fail before succeeding
	perm   bool
	failed int
}

func (f *fake) Encode(batch []*record.Record) ([]target.Payload, []string) {
	var keys []string
	var errs []string
	for _, r := range batch {
		if r.Key == "BAD" {
			errs = append(errs, "bad")
			continue
		}
		keys = append(keys, r.Key)
	}
	if len(keys) == 0 {
		return nil, errs
	}
	b, _ := json.Marshal(keys)
	return []target.Payload{{Body: b, Records: len(keys)}}, errs
}

func (f *fake) Send(ctx context.Context, p target.Payload) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail > 0 {
		f.fail--
		f.failed++
		if f.perm {
			return target.Permanent("bad data")
		}
		return errors.New("down")
	}
	var keys []string
	_ = json.Unmarshal(p.Body, &keys)
	f.sent = append(f.sent, keys)
	return nil
}

func (f *fake) Test(context.Context, *record.Record) (string, error) { return "ok", nil }
func (f *fake) Preview([]*record.Record) (string, []string)          { return "", nil }

func (f *fake) batches() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.sent...)
}

func ev(key string) *record.Record {
	return record.Event(time.Now(), "HmIP-RF", "A:1", key, json.RawMessage("1"), "FLOAT", nil)
}

func cfg(id string, sel string) config.Target {
	t := config.DefaultTarget("webhook")
	t.ID, t.Name, t.Selection = id, id, sel
	t.Webhook.URL = "http://x/"
	t.Batch.Size, t.Batch.IntervalMS = 3, 200
	t.Retry.MinMS, t.Retry.MaxMS = 100, 200
	t.Queue.MaxRecords = 100
	_ = t.Validate()
	return t
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func TestBatchingAndOrder(t *testing.T) {
	f := &fake{}
	m := New(nil)
	m.Build = func(config.Target) (target.Target, error) { return f, nil }
	defer m.Stop()
	m.Apply([]config.Target{cfg("a", "s")}, []selection.Selection{{ID: "s", Name: "s", Keys: []string{"K*"}}})
	for _, k := range []string{"K1", "K2", "K3", "K4", "X1"} {
		m.Dispatch(ev(k))
	}
	waitFor(t, func() bool { return len(f.batches()) == 2 })
	b := f.batches()
	if fmt.Sprint(b) != "[[K1 K2 K3] [K4]]" {
		t.Errorf("batches: %v", b)
	}
	st := m.Status()[0]
	if st.Sent != 4 || st.Batches != 2 || st.State != "ok" || !st.Connected || st.Queued != 0 {
		t.Errorf("status: %+v", st)
	}
}

func TestRetryThenSuccess(t *testing.T) {
	f := &fake{fail: 2}
	m := New(nil)
	m.Build = func(config.Target) (target.Target, error) { return f, nil }
	defer m.Stop()
	m.Apply([]config.Target{cfg("a", "")}, nil)
	m.Dispatch(ev("K1"))
	waitFor(t, func() bool { return m.Status()[0].State == "retrying" })
	st := m.Status()[0]
	if st.Connected || st.LastError != "down" || st.Queued != 1 || st.InFlight != 1 {
		t.Errorf("retrying: %+v", st)
	}
	waitFor(t, func() bool { return len(f.batches()) == 1 })
	st = m.Status()[0]
	if st.Retried != 2 || st.Sent != 1 || st.State != "ok" {
		t.Errorf("after: %+v", st)
	}
}

func TestPermanentDrops(t *testing.T) {
	f := &fake{fail: 1, perm: true}
	m := New(nil)
	m.Build = func(config.Target) (target.Target, error) { return f, nil }
	defer m.Stop()
	m.Apply([]config.Target{cfg("a", "")}, nil)
	m.Dispatch(ev("K1"))
	m.Dispatch(ev("BAD"))
	waitFor(t, func() bool { return m.Status()[0].Dropped == 2 })
	st := m.Status()[0]
	if st.Sent != 0 || st.RenderErrors != 1 || st.Queued != 0 || st.LastError != "bad data" {
		t.Errorf("%+v", st)
	}
}

func TestOverflowAndClear(t *testing.T) {
	f := &fake{fail: 1000}
	m := New(nil)
	m.Build = func(config.Target) (target.Target, error) { return f, nil }
	defer m.Stop()
	c := cfg("a", "")
	c.Queue.MaxRecords = 5
	c.Batch.Size = 2
	m.Apply([]config.Target{c}, nil)
	for i := 0; i < 10; i++ {
		m.Dispatch(ev(fmt.Sprint("K", i)))
	}
	waitFor(t, func() bool { s := m.Status()[0]; return s.Overflow == 5 && s.InFlight == 2 })
	if !m.Clear("a") {
		t.Fatal("clear")
	}
	st := m.Status()[0]
	if st.Queued != 2 || st.Dropped != 8 {
		t.Errorf("after clear: %+v", st)
	}
	if m.Clear("nope") {
		t.Error("unknown cleared")
	}
}

func TestApplyReconciles(t *testing.T) {
	f := &fake{}
	built := 0
	m := New(nil)
	m.Build = func(config.Target) (target.Target, error) { built++; return f, nil }
	defer m.Stop()
	a := cfg("a", "")
	m.Apply([]config.Target{a}, nil)
	m.Apply([]config.Target{a}, nil) // unchanged: not rebuilt
	if built != 1 {
		t.Errorf("built %d", built)
	}
	a.Name = "renamed"
	m.Apply([]config.Target{a}, nil)
	if built != 2 || m.Status()[0].Name != "renamed" {
		t.Errorf("rename: built %d %+v", built, m.Status())
	}
	a.Enabled = false
	m.Apply([]config.Target{a}, nil)
	if st := m.Status()[0]; st.State != "disabled" || st.Running {
		t.Errorf("disabled: %+v", st)
	}
	m.Dispatch(ev("K1"))
	if m.Status()[0].Queued != 0 {
		t.Error("disabled target queued")
	}
	m.Build = func(config.Target) (target.Target, error) { return nil, errors.New("boom") }
	a.Enabled = true
	m.Apply([]config.Target{a}, nil)
	if st := m.Status()[0]; st.State != "error" || st.LastError != "boom" {
		t.Errorf("error: %+v", st)
	}
	m.Apply(nil, nil)
	if len(m.Status()) != 0 {
		t.Error("removed target still there")
	}
}

func TestQueueKeptAcrossReconfigure(t *testing.T) {
	f := &fake{fail: 1000}
	m := New(nil)
	m.Build = func(config.Target) (target.Target, error) { return f, nil }
	defer m.Stop()
	a := cfg("a", "")
	m.Apply([]config.Target{a}, nil)
	m.Dispatch(ev("K1"))
	m.Dispatch(ev("K2"))
	waitFor(t, func() bool { return m.Status()[0].State == "retrying" })
	a.Retry.MaxMS = 300
	f2 := &fake{}
	m.Build = func(config.Target) (target.Target, error) { return f2, nil }
	m.Apply([]config.Target{a}, nil)
	waitFor(t, func() bool { return len(f2.batches()) == 1 })
	if fmt.Sprint(f2.batches()) != "[[K1 K2]]" {
		t.Errorf("%v", f2.batches())
	}
}

func TestBackoff(t *testing.T) {
	w := &worker{cfg: cfg("a", "")}
	w.cfg.Retry.MinMS, w.cfg.Retry.MaxMS = 1000, 8000
	for i, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second} {
		d := w.backoff(i)
		if d < time.Duration(float64(want)*0.74) || d > time.Duration(float64(want)*1.26) {
			t.Errorf("attempt %d: %s", i, d)
		}
	}
}
