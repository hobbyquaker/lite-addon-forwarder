package logsource

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

func TestParseAudit(t *testing.T) {
	cases := []struct {
		line  string
		msg   string
		attrs map[string]string
		ok    bool
	}{
		{`api: change method=PUT route=/api/system/v1/network status=200 user=admin remote=192.0.2.10`, "api: change", map[string]string{"method": "PUT", "route": "/api/system/v1/network", "status": "200", "user": "admin", "remote": "192.0.2.10"}, true},
		{`addon installed: forwarder version="0.3.0 beta" user=admin remote="[2001:db8::1]:4711"`, "addon installed: forwarder", map[string]string{"version": "0.3.0 beta", "user": "admin", "remote": "[2001:db8::1]:4711"}, true},
		{`auth: login user=basti remote=192.0.2.3 method=password`, "auth: login", map[string]string{"user": "basti", "remote": "192.0.2.3", "method": "password"}, true},
		{`firewall: rule a=b applied user=admin remote=10.0.0.1`, "firewall: rule a=b applied", nil, true},
		{`rfd: duty cycle 12%`, "", nil, false},
		{`something user=admin`, "", nil, false},
		{`stream: client stalled url=http://x/ attempts=3`, "", nil, false},
		{`user=admin remote=1.2.3.4`, "", nil, true},
	}
	for _, c := range cases {
		msg, attrs, ok := ParseAudit(c.line)
		if ok != c.ok {
			t.Errorf("%q: ok %v", c.line, ok)
			continue
		}
		if !ok {
			continue
		}
		if msg != c.msg {
			t.Errorf("%q: msg %q", c.line, msg)
		}
		for k, v := range c.attrs {
			if attrs[k] != v {
				t.Errorf("%q: %s=%q want %q", c.line, k, attrs[k], v)
			}
		}
	}
}

// fakeStream is occulited's log stream: the lines it has, sent as SSE; after a cursor only the
// later ones; the request's query is recorded.
type fakeStream struct {
	mu      sync.Mutex
	lines   []Line
	queries []string
	hold    chan struct{}
	status  int
}

func (f *fakeStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.queries = append(f.queries, r.URL.RawQuery)
	status := f.status
	lines := append([]Line{}, f.lines...)
	f.mu.Unlock()
	if status != 0 {
		http.Error(w, "no", status)
		return
	}
	after := r.URL.Query().Get("after")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl := w.(http.Flusher)
	skip := after != ""
	for _, l := range lines {
		if skip {
			if l.Cursor == after {
				skip = false
			}
			continue
		}
		b, _ := json.Marshal(l)
		fmt.Fprintf(w, "data: %s\n\n", b)
	}
	fl.Flush()
	select {
	case <-f.hold:
	case <-r.Context().Done():
	}
}

func TestFollow(t *testing.T) {
	fs := &fakeStream{hold: make(chan struct{})}
	fs.lines = []Line{
		{Timestamp: "2026-10-30T12:00:00.000001Z", Tag: "rfd", Unit: "rfd.service", Severity: "info", PID: 100, Message: "duty cycle 3%", Cursor: "c1"},
		{Timestamp: "2026-10-30T12:00:01Z", Tag: "occulited", Unit: "occulited.service", Severity: "info", PID: 200, Message: "api: change method=PUT route=/api/system/v1/network status=200 user=admin remote=192.0.2.10", Cursor: "c2"},
		{Timestamp: "2026-10-30T12:00:02Z", Tag: "occulited", Unit: "occulited.service", Severity: "debug", Message: "stream: tick", Cursor: "c3"},
	}
	mux := http.NewServeMux()
	mux.Handle("/api/system/v1/log/stream", fs)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var mu sync.Mutex
	var got []*record.Record
	wants := map[string]bool{"journal": true, "audit": true}
	f := New(Options{SystemURL: srv.URL + "/", Wants: func(s string) bool { mu.Lock(); defer mu.Unlock(); return wants[s] }, Emit: func(r *record.Record) {
		mu.Lock()
		got = append(got, r)
		mu.Unlock()
	}, Log: t.Logf})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)

	wait := func(n int) {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			mu.Lock()
			l := len(got)
			mu.Unlock()
			if l >= n {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("records: %d, want %d", len(got), n)
	}
	wait(4)
	mu.Lock()
	j := got[0]
	a := got[2]
	mu.Unlock()
	if j.Kind != record.KindLog || j.Stream != "journal" || j.LogUnit != "rfd.service" || j.Priority != "info" || j.Message != "duty cycle 3%" || j.Fields["tag"] != "rfd" || j.Fields["pid"] != "100" || j.TS.Nanosecond() != 1000 {
		t.Errorf("journal: %+v", j)
	}
	if a.Stream != "audit" || a.Message != "api: change" || a.Fields["user"] != "admin" || a.Fields["route"] != "/api/system/v1/network" || a.LogUnit != "occulited.service" {
		t.Errorf("audit: %+v", a)
	}
	st := f.Stats()
	if !st.Following || st.Lines != 3 || st.Audit != 1 || strings.Join(st.Streams, ",") != "journal,audit" {
		t.Errorf("stats: %+v", st)
	}
	if u := f.Units(); strings.Join(u, ",") != "occulited.service,rfd.service" {
		t.Errorf("units: %v", u)
	}
	// the stream drops: the follower resumes after the last cursor and gets only the new line
	fs.mu.Lock()
	fs.lines = append(fs.lines, Line{Timestamp: "2026-10-30T12:00:03Z", Tag: "rfd", Unit: "rfd.service", Severity: "err", Message: "late", Cursor: "c4"})
	fs.mu.Unlock()
	close(fs.hold)
	fs.hold = make(chan struct{})
	wait(5)
	mu.Lock()
	if got[4].Message != "late" || len(got) != 5 {
		t.Errorf("after resume: %d %v", len(got), got[4])
	}
	mu.Unlock()
	fs.mu.Lock()
	if len(fs.queries) != 2 || !strings.Contains(fs.queries[1], "after=c3") || strings.Contains(fs.queries[0], "tag=") {
		t.Errorf("queries: %v", fs.queries)
	}
	fs.mu.Unlock()
	if st := f.Stats(); st.Reconnects != 1 {
		t.Errorf("reconnects: %+v", st)
	}
	// only the audit stream wanted: the request narrows to occulited's lines
	mu.Lock()
	wants["journal"] = false
	mu.Unlock()
	f.Kick()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		fs.mu.Lock()
		n := len(fs.queries)
		fs.mu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	fs.mu.Lock()
	if len(fs.queries) < 3 || !strings.Contains(fs.queries[2], "tag=occulited") || !strings.Contains(fs.queries[2], "after=c4") {
		t.Errorf("audit only: %v", fs.queries)
	}
	fs.mu.Unlock()
	// nothing wanted: the stream closes
	mu.Lock()
	wants["audit"] = false
	mu.Unlock()
	f.Kick()
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && f.Stats().Following {
		time.Sleep(20 * time.Millisecond)
	}
	if st := f.Stats(); st.Following || len(st.Streams) != 0 {
		t.Errorf("off: %+v", st)
	}
}

func TestFollowForbidden(t *testing.T) {
	fs := &fakeStream{hold: make(chan struct{}), status: 403}
	mux := http.NewServeMux()
	mux.Handle("/api/system/v1/log/stream", fs)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	f := New(Options{SystemURL: srv.URL, Log: t.Logf})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.Run(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && f.Stats().LastError == "" {
		time.Sleep(20 * time.Millisecond)
	}
	if st := f.Stats(); !strings.Contains(st.LastError, "logs:read") || st.Following {
		t.Errorf("forbidden: %+v", st)
	}
}
