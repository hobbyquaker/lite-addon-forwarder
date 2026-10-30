// Package devserver runs the whole addon against a fake openccu-lite for the UI's end-to-end
// tests and for development: FORWARDER_DEV_PORT=8295 go test -count=1 -timeout 0 -run TestServe
// ./internal/devserver serves the built app (ui/dist) at /addons/forwarder/ on that port, a
// webhook receiver at /dev/hook (its bodies at /dev/hook/bodies), an event pusher at
// /dev/push?address=000A:1&key=ACTUAL_TEMPERATURE&value=21.5, a log line pusher at
// /dev/log?message=…, a syslog (UDP) and a GELF (TCP) receiver with their messages at
// /dev/syslog/messages and /dev/gelf/messages, a fake Loki at /dev/loki (its pushes at
// /dev/loki/pushes), and /dev/reset, until it is killed.
package devserver

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource/fakelite"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/httpapi"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/logsource"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/metrics"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/pipeline"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/source"
)

// World fills a fake with two devices: a thermostat with two channels and a key with two.
func World(lite *fakelite.Server) {
	lite.Interfaces = []string{"HmIP-RF", "BidCos-RF"}
	lite.Devices["HmIP-RF"] = []map[string]any{
		{"ADDRESS": "000A1BE9A1B2C3", "TYPE": "HmIP-eTRV-2", "PARENT": "", "CHILDREN": []string{"000A1BE9A1B2C3:0", "000A1BE9A1B2C3:1"}, "FIRMWARE": "2.0", "VERSION": 1},
		{"ADDRESS": "000A1BE9A1B2C3:0", "TYPE": "MAINTENANCE", "PARENT": "000A1BE9A1B2C3", "FIRMWARE": "2.0", "VERSION": 1},
		{"ADDRESS": "000A1BE9A1B2C3:1", "TYPE": "HEATING_CLIMATECONTROL_TRANSCEIVER", "PARENT": "000A1BE9A1B2C3", "FIRMWARE": "2.0", "VERSION": 1},
	}
	lite.Devices["BidCos-RF"] = []map[string]any{
		{"ADDRESS": "MEQ0123456", "TYPE": "HM-PB-2-WM55", "PARENT": "", "CHILDREN": []string{"MEQ0123456:0", "MEQ0123456:1", "MEQ0123456:2"}, "FIRMWARE": "1.0", "VERSION": 1},
		{"ADDRESS": "MEQ0123456:0", "TYPE": "MAINTENANCE", "PARENT": "MEQ0123456", "FIRMWARE": "1.0", "VERSION": 1},
		{"ADDRESS": "MEQ0123456:1", "TYPE": "KEY", "PARENT": "MEQ0123456", "FIRMWARE": "1.0", "VERSION": 1},
		{"ADDRESS": "MEQ0123456:2", "TYPE": "KEY", "PARENT": "MEQ0123456", "FIRMWARE": "1.0", "VERSION": 1},
	}
	lite.Paramsets["HmIP-RF/HmIP-eTRV-2/2.0/1/MAINTENANCE/VALUES"] = map[string]any{
		"LOW_BAT": map[string]any{"TYPE": "BOOL", "OPERATIONS": 5}, "UNREACH": map[string]any{"TYPE": "BOOL", "OPERATIONS": 5}, "RSSI_DEVICE": map[string]any{"TYPE": "INTEGER", "OPERATIONS": 5, "UNIT": "dBm"},
	}
	lite.Paramsets["HmIP-RF/HmIP-eTRV-2/2.0/1/HEATING_CLIMATECONTROL_TRANSCEIVER/VALUES"] = map[string]any{
		"ACTUAL_TEMPERATURE":    map[string]any{"TYPE": "FLOAT", "OPERATIONS": 5, "UNIT": "°C"},
		"SET_POINT_TEMPERATURE": map[string]any{"TYPE": "FLOAT", "OPERATIONS": 7, "UNIT": "°C"},
		"SET_POINT_MODE":        map[string]any{"TYPE": "ENUM", "OPERATIONS": 5, "VALUE_LIST": []string{"AUTO", "MANU"}},
		"LEVEL":                 map[string]any{"TYPE": "FLOAT", "OPERATIONS": 5, "UNIT": "100%"},
		"BOOST_MODE":            map[string]any{"TYPE": "BOOL", "OPERATIONS": 7},
	}
	lite.Paramsets["BidCos-RF/HM-PB-2-WM55/1.0/1/MAINTENANCE/VALUES"] = map[string]any{
		"LOWBAT": map[string]any{"TYPE": "BOOL", "OPERATIONS": 5}, "UNREACH": map[string]any{"TYPE": "BOOL", "OPERATIONS": 5},
	}
	lite.Paramsets["BidCos-RF/HM-PB-2-WM55/1.0/1/KEY/VALUES"] = map[string]any{
		"PRESS_SHORT": map[string]any{"TYPE": "ACTION", "OPERATIONS": 4}, "PRESS_LONG": map[string]any{"TYPE": "ACTION", "OPERATIONS": 4},
	}
	lite.State = []map[string]any{
		{"interface": "HmIP-RF", "address": "000A1BE9A1B2C3:1", "datapoint": "ACTUAL_TEMPERATURE", "value": 20.5, "ts": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339), "confirmed": true, "source": "event"},
		{"interface": "HmIP-RF", "address": "000A1BE9A1B2C3:0", "datapoint": "LOW_BAT", "value": false, "ts": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "confirmed": true, "source": "event"},
	}
	lite.EventID = "boot1-1"
	lite.Meta["enums"] = map[string]any{
		"room":     map[string]any{"name": map[string]any{"de": "Räume", "en": "Rooms"}, "tree": []any{map[string]any{"id": "bad", "name": map[string]any{"de": "Bad", "en": "Bathroom"}}, map[string]any{"id": "flur", "name": map[string]any{"de": "Flur", "en": "Hallway"}}}},
		"function": map[string]any{"name": map[string]any{"de": "Gewerke", "en": "Functions"}, "tree": []any{map[string]any{"id": "heizung", "name": map[string]any{"de": "Heizung", "en": "Heating"}}, map[string]any{"id": "licht", "name": map[string]any{"de": "Licht", "en": "Light"}}}},
	}
	lite.SetName("HmIP-RF.000A1BE9A1B2C3", "Thermostat Bad")
	lite.SetName("HmIP-RF.000A1BE9A1B2C3:1", "Heizung Bad", "room/bad", "function/heizung")
	lite.SetName("BidCos-RF.MEQ0123456", "Taster Flur", "room/flur")
	lite.SetName("BidCos-RF.MEQ0123456:1", "Taster Flur oben", "room/flur", "function/licht")
}

type hook struct {
	mu     sync.Mutex
	bodies []json.RawMessage
}

// fakeES answers _bulk (every item taken) and _index_template at /dev/es.
func fakeES(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/dev/es/_bulk":
		b, _ := io.ReadAll(r.Body)
		var items []map[string]any
		for i, line := range bytesLines(b) {
			if i%2 == 0 && len(line) > 0 {
				items = append(items, map[string]any{"index": map[string]any{"status": 201, "_index": "x"}})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"took": 1, "errors": false, "items": items})
	case len(r.URL.Path) > len("/dev/es/_index_template/") && r.URL.Path[:len("/dev/es/_index_template/")] == "/dev/es/_index_template/":
		_, _ = w.Write([]byte(`{"acknowledged":true}`))
	default:
		http.NotFound(w, r)
	}
}

func bytesLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i, c := range b {
		if c == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}

// fakeJournal is occulited's log stream: a few lines at the start, then whatever /dev/log pushes.
type fakeJournal struct {
	mu    sync.Mutex
	lines []logsource.Line
	subs  map[chan logsource.Line]bool
	n     int
}

func newFakeJournal() *fakeJournal {
	j := &fakeJournal{subs: map[chan logsource.Line]bool{}}
	j.push("rfd", "rfd.service", "info", "rfd: duty cycle 3%")
	j.push("occulited", "occulited.service", "info", "api: change method=PUT route=/api/system/v1/network status=200 user=admin remote=192.0.2.10")
	j.push("hmipserver", "hmipserver.service", "warning", "hmipserver: a warning")
	return j
}

func (j *fakeJournal) push(tag, unit, severity, message string) {
	j.mu.Lock()
	j.n++
	l := logsource.Line{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), Tag: tag, Unit: unit, Severity: severity, PID: 100 + j.n, Message: message, Cursor: fmt.Sprintf("c%d", j.n), Host: "devbox"}
	j.lines = append(j.lines, l)
	for ch := range j.subs {
		select {
		case ch <- l:
		default:
		}
	}
	j.mu.Unlock()
}

func (j *fakeJournal) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	tag := r.URL.Query().Get("tag")
	after := r.URL.Query().Get("after")
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(200)
	fl := w.(http.Flusher)
	ch := make(chan logsource.Line, 64)
	j.mu.Lock()
	j.subs[ch] = true
	skip := after != ""
	for _, l := range j.lines {
		if skip {
			if l.Cursor == after {
				skip = false
			}
			continue
		}
		if tag == "" || l.Tag == tag {
			b, _ := json.Marshal(l)
			fmt.Fprintf(w, "data: %s\n\n", b)
		}
	}
	j.mu.Unlock()
	fl.Flush()
	defer func() {
		j.mu.Lock()
		delete(j.subs, ch)
		j.mu.Unlock()
	}()
	for {
		select {
		case <-r.Context().Done():
			return
		case l := <-ch:
			if tag == "" || l.Tag == tag {
				b, _ := json.Marshal(l)
				fmt.Fprintf(w, "data: %s\n\n", b)
				fl.Flush()
			}
		}
	}
}

// receivers are the syslog (UDP), GELF (TCP, null-delimited) and Loki receivers of the dev
// server, their messages answered as JSON lists.
type receivers struct {
	mu     sync.Mutex
	syslog []string
	gelf   []json.RawMessage
	loki   []json.RawMessage
	udp    net.PacketConn
	tcp    net.Listener
}

func newReceivers(t *testing.T) *receivers {
	rc := &receivers{}
	var err error
	if rc.udp, err = net.ListenPacket("udp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	if rc.tcp, err = net.Listen("tcp", "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	go func() {
		buf := make([]byte, 65536)
		for {
			n, _, err := rc.udp.ReadFrom(buf)
			if err != nil {
				return
			}
			rc.mu.Lock()
			rc.syslog = append(rc.syslog, string(buf[:n]))
			rc.mu.Unlock()
		}
	}()
	go func() {
		for {
			c, err := rc.tcp.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					doc, err := r.ReadBytes(0)
					doc = bytes.TrimSuffix(doc, []byte{0})
					if len(doc) > 0 && json.Valid(doc) {
						rc.mu.Lock()
						rc.gelf = append(rc.gelf, json.RawMessage(doc))
						rc.mu.Unlock()
					}
					if err != nil {
						return
					}
				}
			}()
		}
	}()
	return rc
}

func (rc *receivers) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	rc.mu.Lock()
	defer rc.mu.Unlock()
	switch r.URL.Path {
	case "/dev/syslog/messages":
		_ = json.NewEncoder(w).Encode(rc.syslog)
	case "/dev/gelf/messages":
		_ = json.NewEncoder(w).Encode(nzRaw(rc.gelf))
	case "/dev/loki/pushes":
		_ = json.NewEncoder(w).Encode(nzRaw(rc.loki))
	case "/dev/loki/loki/api/v1/push":
		var body io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			zr, err := gzip.NewReader(r.Body)
			if err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			body = zr
		}
		b, _ := io.ReadAll(body)
		if !json.Valid(b) {
			http.Error(w, "not json", 400)
			return
		}
		rc.loki = append(rc.loki, json.RawMessage(b))
		w.WriteHeader(204)
	case "/dev/syslog/port":
		fmt.Fprint(w, rc.udp.LocalAddr().(*net.UDPAddr).Port)
	case "/dev/gelf/port":
		fmt.Fprint(w, rc.tcp.Addr().(*net.TCPAddr).Port)
	default:
		http.NotFound(w, r)
	}
}

func nzRaw(l []json.RawMessage) []json.RawMessage {
	if l == nil {
		return []json.RawMessage{}
	}
	return l
}

func (rc *receivers) reset() {
	rc.mu.Lock()
	rc.syslog, rc.gelf, rc.loki = nil, nil, nil
	rc.mu.Unlock()
}

// fakeSystem is occulited's system API as the metrics poller and the log follower read it: a
// few statistics and the journal.
func fakeSystem(journal *fakeJournal) *httptest.Server {
	mux := http.NewServeMux()
	mux.Handle("/api/system/v1/log/stream", journal)
	j := func(v any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(v)
		}
	}
	mux.HandleFunc("/api/system/v1/service-messages", j(map[string]any{"count": 2, "messages": []map[string]any{{"interface": "HmIP-RF", "address": "000A1BE9A1B2C3:0", "key": "LOW_BAT"}, {"interface": "BidCos-RF", "address": "MEQ0123456:0", "key": "UNREACH"}}}))
	mux.HandleFunc("/api/system/v1/radio/health", j(map[string]any{"interfaces": []map[string]any{{"interface": "HmIP-RF", "address": "3014F711A0001F58A9A1B2C3", "type": "HmIP-RFUSB", "connected": true, "duty_cycle": 4}}, "errors": map[string]string{}, "rates": map[string]any{"HmIP-RF": []map[string]any{{"t": time.Now().UTC().Format(time.RFC3339), "in": 2.5, "up": true}}}, "busy": false}))
	mux.HandleFunc("/api/system/v1/radio/subscribers/reachability", j(map[string]any{"subscribers": []map[string]any{{"interface": "HmIP-RF", "id": "occulited", "url": "http://127.0.0.1:1999", "reachable": true}}}))
	mux.HandleFunc("/api/system/v1/radio/subscribers/stalls", j(map[string]any{"stalls": []map[string]any{}}))
	mux.HandleFunc("/api/system/v1/services", j(map[string]any{"systemd": true, "services": []map[string]any{{"id": "rfd", "kind": "system", "category": "radio", "running": true, "pid": os.Getpid()}, {"id": "addon-forwarder", "kind": "addon", "category": "addon", "running": true, "pid": os.Getpid()}}}))
	return httptest.NewServer(mux)
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/dev/hook/bodies" {
		h.mu.Lock()
		b, _ := json.Marshal(h.bodies)
		h.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(b)
		return
	}
	if r.URL.Path == "/dev/hook/fail" {
		http.Error(w, "down", 503)
		return
	}
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	h.bodies = append(h.bodies, json.RawMessage(b))
	h.mu.Unlock()
	w.WriteHeader(204)
}

func TestServe(t *testing.T) {
	port, _ := strconv.Atoi(os.Getenv("FORWARDER_DEV_PORT"))
	if port == 0 {
		t.Skip("FORWARDER_DEV_PORT not set")
	}
	lite := fakelite.New()
	defer lite.Close()
	lite.Token = "tok"
	World(lite)

	dir := t.TempDir()
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	client := litesource.New(lite.URL, addonkit.StaticToken("tok"))
	pipe := pipeline.New(t.Logf)
	pipe.SpillDir = filepath.Join(dir, "var", "spill")
	hub := httpapi.NewHub()
	pipe.OnChange = hub.Broadcast
	apply := func() { pipe.Apply(store.TargetsWithSecrets(), store.Selections()) }
	store.OnChange = func() { apply(); hub.Broadcast() }
	apply()
	src := source.New(source.Options{Client: client, Emit: pipe.Dispatch, Log: t.Logf, Snapshot: func() bool { return store.Settings().Snapshot }, Lang: func() string { return store.Settings().Lang }})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx)

	journal := newFakeJournal()
	sys := fakeSystem(journal)
	defer sys.Close()
	poller := metrics.New(metrics.Options{SystemURL: sys.URL, Interval: func() time.Duration { return time.Duration(store.Settings().MetricsIntervalS) * time.Second }, Wants: pipe.WantsMetric, Emit: src.Emit, Events: src.EventsByInterface, Log: t.Logf})
	go poller.Run(ctx)
	logs := logsource.New(logsource.Options{SystemURL: sys.URL, Wants: pipe.WantsLog, Emit: src.Emit, Log: t.Logf})
	go logs.Run(ctx)
	store.OnChange = func() { apply(); poller.Kick(); logs.Kick(); hub.Broadcast() }

	www := filepath.Join("..", "..", "ui", "dist")
	h := httpapi.New(httpapi.Options{BasePath: "/addons/forwarder", WWW: www, Store: store, Pipeline: pipe, Source: src, Metrics: poller, Logs: logs, Version: "dev", Started: time.Now(), Hub: hub})
	hk := &hook{}
	rc := newReceivers(t)
	defer rc.udp.Close()
	defer rc.tcp.Close()
	mux := http.NewServeMux()
	mux.Handle("/addons/forwarder/", h)
	mux.Handle("/dev/hook", hk)
	mux.Handle("/dev/hook/", hk)
	mux.Handle("/dev/syslog/", rc)
	mux.Handle("/dev/gelf/", rc)
	mux.Handle("/dev/loki/", rc)
	mux.HandleFunc("/dev/log", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		tag, unit, sev := q.Get("tag"), q.Get("unit"), q.Get("severity")
		if tag == "" {
			tag = "rfd"
		}
		if unit == "" {
			unit = tag + ".service"
		}
		if sev == "" {
			sev = "info"
		}
		journal.push(tag, unit, sev, q.Get("message"))
		w.WriteHeader(204)
	})
	mux.HandleFunc("/dev/es/", fakeES)
	mux.HandleFunc("/dev/push", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		var v any
		if err := json.Unmarshal([]byte(q.Get("value")), &v); err != nil {
			v = q.Get("value")
		}
		iface := q.Get("interface")
		if iface == "" {
			iface = "HmIP-RF"
		}
		lite.PushEvent(iface, q.Get("address"), q.Get("key"), v)
		w.WriteHeader(204)
	})
	// /dev/reset empties the configuration and the receiver, so every Playwright project starts alike
	mux.HandleFunc("/dev/reset", func(w http.ResponseWriter, r *http.Request) {
		for _, t := range store.Targets() {
			_ = store.DeleteTarget(t.ID)
		}
		for _, s := range store.Selections() {
			_ = store.DeleteSelection(s.ID)
		}
		hk.mu.Lock()
		hk.bodies = nil
		hk.mu.Unlock()
		rc.reset()
		w.WriteHeader(204)
	})
	mux.HandleFunc("/dev/rename", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		lite.SetName(q.Get("ref"), q.Get("name"), q["enum"]...)
		w.WriteHeader(204)
	})
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dev server on http://127.0.0.1:%d/addons/forwarder/ (fake system %s, syslog udp %s, gelf tcp %s)", port, lite.URL, strings.TrimPrefix(rc.udp.LocalAddr().String(), "127.0.0.1"), strings.TrimPrefix(rc.tcp.Addr().String(), "127.0.0.1"))
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
