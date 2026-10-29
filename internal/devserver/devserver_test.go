// Package devserver runs the whole addon against a fake openccu-lite for the UI's end-to-end
// tests and for development: FORWARDER_DEV_PORT=8295 go test -count=1 -timeout 0 -run TestServe
// ./internal/devserver serves the built app (ui/dist) at /addons/forwarder/ on that port, a
// webhook receiver at /dev/hook (its bodies at /dev/hook/bodies) and an event pusher at
// /dev/push?address=000A:1&key=ACTUAL_TEMPERATURE&value=21.5, and /dev/reset, until it is killed.
package devserver

import (
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
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource/fakelite"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/httpapi"
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

// fakeSystem is occulited's system API as the metrics poller reads it: a few statistics.
func fakeSystem() *httptest.Server {
	mux := http.NewServeMux()
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

	sys := fakeSystem()
	defer sys.Close()
	poller := metrics.New(metrics.Options{SystemURL: sys.URL, Interval: func() time.Duration { return time.Duration(store.Settings().MetricsIntervalS) * time.Second }, Wants: pipe.WantsMetric, Emit: src.Emit, Events: src.EventsByInterface, Log: t.Logf})
	go poller.Run(ctx)
	store.OnChange = func() { apply(); poller.Kick(); hub.Broadcast() }

	www := filepath.Join("..", "..", "ui", "dist")
	h := httpapi.New(httpapi.Options{BasePath: "/addons/forwarder", WWW: www, Store: store, Pipeline: pipe, Source: src, Metrics: poller, Version: "dev", Started: time.Now(), Hub: hub})
	hk := &hook{}
	mux := http.NewServeMux()
	mux.Handle("/addons/forwarder/", h)
	mux.Handle("/dev/hook", hk)
	mux.Handle("/dev/hook/", hk)
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
	t.Logf("dev server on http://127.0.0.1:%d/addons/forwarder/ (fake system %s)", port, lite.URL)
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	if err := srv.Serve(l); err != nil && err != http.ErrServerClosed {
		t.Fatal(err)
	}
}
