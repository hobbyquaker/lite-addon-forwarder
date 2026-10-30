package target

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

type lokiFake struct {
	mu     sync.Mutex
	pushes []push
	status int
	tenant string
	auth   string
}

func (f *lokiFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tenant = r.Header.Get("X-Scope-OrgID")
	f.auth = r.Header.Get("Authorization")
	if r.URL.Path != "/loki/api/v1/push" {
		http.NotFound(w, r)
		return
	}
	var body io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body = zr
	}
	var p push
	if err := json.NewDecoder(body).Decode(&p); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if f.status != 0 {
		http.Error(w, "nope", f.status)
		return
	}
	f.pushes = append(f.pushes, p)
	w.WriteHeader(204)
}

func lokiTarget(t *testing.T, url string, mod func(*config.Target)) *Loki {
	t.Helper()
	cfg := config.DefaultTarget("loki")
	cfg.Name = "loki"
	cfg.Loki.URL = url
	if mod != nil {
		mod(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	l, err := NewLoki(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLoki(t *testing.T) {
	f := &lokiFake{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	l := lokiTarget(t, srv.URL+"/", func(c *config.Target) {
		c.Loki.TenantID = "home"
		c.Loki.Auth = "bearer"
		c.Secrets = map[string]string{"token": "tk"}
	})
	ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	e1 := record.Event(ts.Add(2*time.Second), "HmIP-RF", "ABC:1", "LEVEL", json.RawMessage(`0.5`), "FLOAT", nil)
	e1.ChannelName, e1.DeviceName, e1.Rooms, e1.Source = "Rollo", "Rollo", []string{"Bad"}, "event"
	e2 := record.Event(ts, "HmIP-RF", "ABC:1", "STATE", json.RawMessage(`true`), "BOOL", nil)
	e2.Source = "event"
	m := record.Metric(ts, "cpu", "load1", 0.5, map[string]string{"host": "ccu"})
	lg := record.Log(ts, "journal", "rfd.service", "info", "hello world", map[string]string{"pid": "7"})
	payloads, errs := l.Encode([]*record.Record{e1, e2, m, lg})
	if len(errs) != 0 || len(payloads) != 1 || payloads[0].Records != 4 {
		t.Fatalf("encode %v %d", errs, len(payloads))
	}
	var p push
	_ = json.Unmarshal(payloads[0].Body, &p)
	if len(p.Streams) != 3 {
		t.Fatalf("streams: %d %s", len(p.Streams), payloads[0].Body)
	}
	ev := p.Streams[0]
	if ev.Stream["job"] != "homematic" || ev.Stream["kind"] != "event" || ev.Stream["interface"] != "HmIP-RF" || len(ev.Stream) != 3 {
		t.Errorf("labels %v", ev.Stream)
	}
	// sorted by time: STATE (ts) before LEVEL (ts+2s)
	if len(ev.Values) != 2 || ev.Values[0][0] != strconv.FormatInt(ts.UnixNano(), 10) || !strings.HasPrefix(ev.Values[0][1].(string), "address=ABC:1 key=STATE value=true type=BOOL") || !strings.Contains(ev.Values[1][1].(string), `device_name="Rollo"`) || !strings.Contains(ev.Values[1][1].(string), `room="Bad"`) {
		t.Errorf("values %v", ev.Values)
	}
	// the metric's labels become structured metadata, the log line's fields too
	mt := p.Streams[1]
	if mt.Stream["group"] != "cpu" || len(mt.Values[0]) != 3 || mt.Values[0][2].(map[string]any)["host"] != "ccu" || mt.Values[0][1] != "group=cpu name=load1 value=0.5 host=\"ccu\"" {
		t.Errorf("metric %v", mt)
	}
	lgs := p.Streams[2]
	if lgs.Stream["unit"] != "rfd.service" || lgs.Stream["priority"] != "info" || lgs.Values[0][1] != "hello world" || lgs.Values[0][2].(map[string]any)["pid"] != "7" {
		t.Errorf("log %v", lgs)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	if len(f.pushes) != 1 || f.tenant != "home" || f.auth != "Bearer tk" {
		t.Errorf("push %d %q %q", len(f.pushes), f.tenant, f.auth)
	}
	f.mu.Unlock()
	if msg, err := l.Test(ctx, Sample()); err != nil || !strings.HasPrefix(msg, "HTTP 204") {
		t.Errorf("test: %q %v", msg, err)
	}
	// 400 is permanent, 429 retried
	f.mu.Lock()
	f.status = 400
	f.mu.Unlock()
	if err := l.Send(ctx, payloads[0]); err == nil || !IsPermanent(err) {
		t.Errorf("400: %v", err)
	}
	f.mu.Lock()
	f.status = 429
	f.mu.Unlock()
	if err := l.Send(ctx, payloads[0]); err == nil || IsPermanent(err) {
		t.Errorf("429: %v", err)
	}
	text, errs := l.Preview([]*record.Record{e1})
	if len(errs) != 0 || !strings.Contains(text, `"job": "homematic"`) {
		t.Errorf("preview %v %s", errs, text)
	}
	// the json preset
	j := lokiTarget(t, srv.URL, func(c *config.Target) {
		p := config.LokiPresets["json"]
		c.Loki.Labels, c.Loki.Line, c.Loki.Metadata = p.Labels, p.Line, p.Metadata
	})
	payloads, _ = j.Encode([]*record.Record{e1})
	_ = json.Unmarshal(payloads[0].Body, &p)
	if !strings.Contains(p.Streams[0].Values[0][1].(string), `"key":"LEVEL"`) || len(p.Streams[0].Values[0]) != 2 {
		t.Errorf("json %v", p.Streams[0].Values)
	}
	// no label at all is an error per record
	n := lokiTarget(t, srv.URL, func(c *config.Target) { c.Loki.Labels = []config.NamedTemplate{{Name: "x", Value: "{{ group }}"}} })
	if _, errs := n.Encode([]*record.Record{e1}); len(errs) != 1 || !strings.Contains(errs[0], "no label") {
		t.Errorf("no label: %v", errs)
	}
}
