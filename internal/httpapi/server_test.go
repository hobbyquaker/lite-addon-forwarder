package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/ccu-addon-kit/go/addonkit"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource"
	"github.com/hobbyquaker/ccu-addon-kit/go/litesource/fakelite"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/pipeline"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/source"
)

// world is a fake openccu-lite with one thermostat, a webhook receiver and the addon on top.
type world struct {
	lite  *fakelite.Server
	hook  *hookServer
	app   *httptest.Server
	store *config.Store
	pipe  *pipeline.Manager
	src   *source.Source
}

type hookServer struct {
	*httptest.Server
	mu     sync.Mutex
	bodies [][]map[string]any
	paths  []string
}

func newHook() *hookServer {
	h := &hookServer{}
	h.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var recs []map[string]any
		_ = json.Unmarshal(b, &recs)
		h.mu.Lock()
		h.bodies = append(h.bodies, recs)
		h.paths = append(h.paths, r.URL.Path)
		h.mu.Unlock()
		w.WriteHeader(204)
	}))
	return h
}

func (h *hookServer) records() []map[string]any {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []map[string]any
	for _, b := range h.bodies {
		out = append(out, b...)
	}
	return out
}

func newWorld(t *testing.T) *world {
	t.Helper()
	lite := fakelite.New()
	t.Cleanup(lite.Close)
	lite.Token = "tok"
	lite.Interfaces = []string{"HmIP-RF"}
	lite.Devices["HmIP-RF"] = []map[string]any{
		{"ADDRESS": "000A", "TYPE": "HmIP-eTRV-2", "PARENT": "", "CHILDREN": []string{"000A:0", "000A:1"}, "FIRMWARE": "2.0", "VERSION": 1},
		{"ADDRESS": "000A:0", "TYPE": "MAINTENANCE", "PARENT": "000A", "FIRMWARE": "2.0", "VERSION": 1},
		{"ADDRESS": "000A:1", "TYPE": "HEATING_CLIMATECONTROL_TRANSCEIVER", "PARENT": "000A", "FIRMWARE": "2.0", "VERSION": 1},
	}
	lite.Paramsets["HmIP-RF/HmIP-eTRV-2/2.0/1/MAINTENANCE/VALUES"] = map[string]any{
		"LOW_BAT": map[string]any{"TYPE": "BOOL", "OPERATIONS": 5, "UNIT": ""},
		"UNREACH": map[string]any{"TYPE": "BOOL", "OPERATIONS": 5},
	}
	lite.Paramsets["HmIP-RF/HmIP-eTRV-2/2.0/1/HEATING_CLIMATECONTROL_TRANSCEIVER/VALUES"] = map[string]any{
		"ACTUAL_TEMPERATURE": map[string]any{"TYPE": "FLOAT", "OPERATIONS": 5, "UNIT": "°C"},
		"SET_POINT_MODE":     map[string]any{"TYPE": "ENUM", "OPERATIONS": 5, "VALUE_LIST": []string{"AUTO", "MANU"}},
		"BOOST_MODE":         map[string]any{"TYPE": "BOOL", "OPERATIONS": 7},
	}
	lite.State = []map[string]any{{"interface": "HmIP-RF", "address": "000A:1", "datapoint": "ACTUAL_TEMPERATURE", "value": 20.5, "ts": "2026-10-29T10:00:00Z", "confirmed": true, "source": "event"}}
	lite.EventID = "boot1-1"
	lite.Meta["enums"] = map[string]any{"room": map[string]any{"name": map[string]any{"de": "Räume"}, "tree": []any{map[string]any{"id": "bad", "name": map[string]any{"de": "Bad", "en": "Bathroom"}}}}, "function": map[string]any{"name": map[string]any{"de": "Gewerke"}, "tree": []any{map[string]any{"id": "heizung", "name": map[string]any{"de": "Heizung", "en": "Heating"}}}}}
	lite.SetName("HmIP-RF.000A", "Thermostat Bad")
	lite.SetName("HmIP-RF.000A:1", "Heizung Bad", "room/bad", "function/heizung")

	hook := newHook()
	t.Cleanup(hook.Close)

	dir := t.TempDir()
	store, err := config.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	client := litesource.New(lite.URL, addonkit.StaticToken("tok"))
	pipe := pipeline.New(t.Logf)
	hub := NewHub()
	pipe.OnChange = hub.Broadcast
	apply := func() { pipe.Apply(store.TargetsWithSecrets(), store.Selections()) }
	store.OnChange = apply
	apply()
	src := source.New(source.Options{Client: client, Emit: pipe.Dispatch, Log: t.Logf})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { src.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done; pipe.Stop() })
	h := New(Options{BasePath: "/addons/forwarder", WWW: dir, Store: store, Pipeline: pipe, Source: src, Version: "test", Started: time.Now(), Hub: hub})
	app := httptest.NewServer(h)
	t.Cleanup(app.Close)
	w := &world{lite: lite, hook: hook, app: app, store: store, pipe: pipe, src: src}
	waitFor(t, func() bool { return src.Stats().Datapoints == 5 && src.Stats().Connected })
	return w
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

func (w *world) call(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, w.app.URL+"/addons/forwarder"+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(addonkit.HeaderRequest, "1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if out != nil && len(b) > 0 {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, b)
		}
	}
	if res.StatusCode >= 400 {
		t.Logf("%s %s -> %d %s", method, path, res.StatusCode, b)
	}
	return res.StatusCode
}

func TestEndToEnd(t *testing.T) {
	w := newWorld(t)

	// the inventory carries names, rooms, types and keys
	var inv source.Inventory
	w.call(t, "GET", "/api/v1/inventory", nil, &inv)
	if len(inv.Devices) != 1 || inv.Devices[0].Name != "Thermostat Bad" || len(inv.Devices[0].Channels) != 2 || inv.Datapoints != 5 {
		t.Fatalf("inventory: %+v", inv)
	}
	ch := inv.Devices[0].Channels[1]
	if ch.Name != "Heizung Bad" || fmt.Sprint(ch.Rooms) != "[Bad]" || fmt.Sprint(ch.Functions) != "[Heizung]" || len(ch.Keys) != 3 {
		t.Errorf("channel: %+v", ch)
	}
	if fmt.Sprint(inv.Rooms) != "[Bad]" || len(inv.Keys) != 5 || inv.ChannelTypes[0].Name != "HEATING_CLIMATECONTROL_TRANSCEIVER" {
		t.Errorf("inventory lists: %+v", inv)
	}

	// a selection by room, counted live
	var sel SelectionView
	if code := w.call(t, "POST", "/api/v1/selections", map[string]any{"name": "Bad", "kinds": []string{"event"}, "rooms": []string{"Bad"}}, &sel); code != 201 {
		t.Fatalf("create selection: %d", code)
	}
	if sel.Count.Datapoints != 3 || sel.Count.Channels != 1 || sel.Count.Devices != 1 {
		t.Errorf("count: %+v", sel.Count)
	}
	var match Match
	w.call(t, "POST", "/api/v1/selections/match", map[string]any{"name": "x", "keys": []string{"LOW_BAT", "UN*"}}, &match)
	if match.Count.Datapoints != 2 {
		t.Errorf("match: %+v", match.Count)
	}
	// the snapshot's value is among the recent records
	w.call(t, "POST", "/api/v1/selections/match", map[string]any{"name": "x", "sources": []string{"snapshot"}}, &match)
	if match.Count.Datapoints != 5 || len(match.Recent) != 1 || match.Recent[0].Key != "ACTUAL_TEMPERATURE" || match.Recent[0].DeviceName != "Thermostat Bad" {
		t.Errorf("snapshot recent: %+v", match)
	}

	// a webhook target with a bearer token, enabled
	var tv TargetView
	body := map[string]any{"name": "hook", "type": "webhook", "enabled": true, "selection": sel.ID, "batch": map[string]any{"size": 10, "interval_ms": 200}, "webhook": map[string]any{"url": w.hook.URL + "/{{ kind }}", "auth": "bearer"}, "set_secrets": map[string]string{"token": "s3cret"}}
	if code := w.call(t, "POST", "/api/v1/targets", body, &tv); code != 201 {
		t.Fatalf("create target: %d", code)
	}
	if !tv.SecretsSet["token"] || tv.SetSecrets != nil || tv.Status == nil || !tv.Status.Running {
		t.Errorf("target view: %+v", tv)
	}
	// a secret is never answered
	var raw []map[string]any
	w.call(t, "GET", "/api/v1/targets", nil, &raw)
	if b, _ := json.Marshal(raw); strings.Contains(string(b), "s3cret") {
		t.Error("secret in the answer")
	}

	// events flow: the room's channel is forwarded with its labels, the other is not
	w.lite.PushEvent("HmIP-RF", "000A:1", "ACTUAL_TEMPERATURE", 21.5)
	w.lite.PushEvent("HmIP-RF", "000A:0", "LOW_BAT", true)
	w.lite.PushEvent("HmIP-RF", "000A:1", "SET_POINT_MODE", 1)
	waitFor(t, func() bool { return len(w.hook.records()) == 2 })
	recs := w.hook.records()
	if recs[0]["key"] != "ACTUAL_TEMPERATURE" || recs[0]["value"] != 21.5 || recs[0]["unit"] != "°C" || recs[0]["channel_name"] != "Heizung Bad" || recs[0]["device_name"] != "Thermostat Bad" || fmt.Sprint(recs[0]["rooms"]) != "[Bad]" || recs[0]["device_type"] != "HmIP-eTRV-2" || recs[0]["source"] != "event" {
		t.Errorf("record: %v", recs[0])
	}
	if recs[1]["key"] != "SET_POINT_MODE" || recs[1]["value_text"] != "MANU" || recs[1]["value"] != float64(1) || recs[1]["type"] != "ENUM" {
		t.Errorf("enum record: %v", recs[1])
	}
	w.hook.mu.Lock()
	path := w.hook.paths[0]
	w.hook.mu.Unlock()
	if path != "/event" {
		t.Errorf("path %s", path)
	}

	// status
	var st struct {
		Targets []pipeline.Status `json:"targets"`
	}
	w.call(t, "GET", "/api/v1/status", nil, &st)
	if len(st.Targets) != 1 || st.Targets[0].Sent != 2 || st.Targets[0].State != "ok" {
		t.Errorf("status: %+v", st.Targets)
	}

	// a rename follows live
	w.lite.SetName("HmIP-RF.000A:1", "Heizung Bad neu", "room/bad")
	waitFor(t, func() bool {
		var inv source.Inventory
		w.call(t, "GET", "/api/v1/inventory", nil, &inv)
		return inv.Devices[0].Channels[1].Name == "Heizung Bad neu"
	})

	// preview and render
	var pv Preview
	w.call(t, "POST", "/api/v1/targets/preview", body, &pv)
	if pv.Records == 0 || !strings.Contains(pv.Text, `"key":"ACTUAL_TEMPERATURE"`) {
		t.Errorf("preview: %+v", pv)
	}
	var rr struct {
		Error   string     `json:"error"`
		Results []Rendered `json:"results"`
	}
	w.call(t, "POST", "/api/v1/templates/render", map[string]any{"template": "{{ key | lower }}/{{ rooms | join(',') }}", "n": 3}, &rr)
	if rr.Error != "" || len(rr.Results) == 0 || !strings.HasSuffix(rr.Results[0].Output, "/Bad") {
		t.Errorf("render: %+v", rr)
	}
	w.call(t, "POST", "/api/v1/templates/render", map[string]any{"template": "{{ key | nosuch }}"}, &rr)
	if rr.Error == "" {
		t.Error("bad template rendered")
	}

	// the test endpoint
	var tr TestResult
	w.call(t, "POST", "/api/v1/targets/test", body, &tr)
	if !tr.OK || !strings.HasPrefix(tr.Message, "HTTP 204") {
		t.Errorf("test: %+v", tr)
	}
	bad := map[string]any{"name": "x", "type": "influx", "influx": map[string]any{"version": "2", "url": "http://127.0.0.1:9", "org": "o", "bucket": "b", "event": map[string]any{"measurement": "m", "fields": []map[string]any{{"name": "v", "value": "{{ value }}"}}}}, "timeout_s": 1}
	w.call(t, "POST", "/api/v1/targets/test", bad, &tr)
	if tr.OK {
		t.Errorf("unreachable test ok: %+v", tr)
	}

	// disable, clear, delete; the selection is in use until then
	if code := w.call(t, "DELETE", "/api/v1/selections/"+sel.ID, nil, nil); code != 409 {
		t.Errorf("delete used selection: %d", code)
	}
	w.call(t, "POST", "/api/v1/targets/"+tv.ID+"/enabled", map[string]any{"enabled": false}, &tv)
	if tv.Enabled || tv.Status.State != "disabled" {
		t.Errorf("disabled: %+v", tv)
	}
	if code := w.call(t, "POST", "/api/v1/targets/"+tv.ID+"/clear", nil, nil); code != 204 {
		t.Errorf("clear: %d", code)
	}
	if code := w.call(t, "DELETE", "/api/v1/targets/"+tv.ID, nil, nil); code != 204 {
		t.Errorf("delete target: %d", code)
	}
	if code := w.call(t, "DELETE", "/api/v1/selections/"+sel.ID, nil, nil); code != 204 {
		t.Errorf("delete selection: %d", code)
	}
	var h Health
	w.call(t, "GET", "/api/v1/health", nil, &h)
	if h.Targets != 0 || h.Selections != 0 || h.Source.Events != 3 || h.Source.Snapshots != 1 {
		t.Errorf("health: %+v", h)
	}
}

func TestValidationAndErrors(t *testing.T) {
	w := newWorld(t)
	var e map[string]any
	if code := w.call(t, "POST", "/api/v1/targets", map[string]any{"name": "", "type": "webhook", "webhook": map[string]any{"url": "http://x"}}, &e); code != 400 {
		t.Errorf("empty name: %d", code)
	}
	if code := w.call(t, "POST", "/api/v1/targets", map[string]any{"name": "x", "type": "webhook", "selection": "nope", "webhook": map[string]any{"url": "http://x"}}, &e); code != 400 {
		t.Errorf("unknown selection: %d", code)
	}
	if code := w.call(t, "POST", "/api/v1/targets", map[string]any{"name": "x", "type": "influx", "influx": map[string]any{"version": "1", "url": "http://x"}}, &e); code != 400 || !strings.Contains(fmt.Sprint(e["message"]), "database") {
		t.Errorf("influx v1 without db: %d %v", code, e)
	}
	if code := w.call(t, "PUT", "/api/v1/targets/nope", map[string]any{"name": "x", "type": "webhook", "webhook": map[string]any{"url": "http://x"}}, &e); code != 404 {
		t.Errorf("unknown target: %d", code)
	}
	if code := w.call(t, "POST", "/api/v1/selections", map[string]any{"name": "x", "keys": []string{"["}}, &e); code != 400 {
		t.Errorf("bad glob: %d", code)
	}
	// CSRF: a write without the header is refused
	req, _ := http.NewRequest("POST", w.app.URL+"/addons/forwarder/api/v1/selections", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Content-Type", "application/json")
	res, _ := http.DefaultClient.Do(req)
	if res.StatusCode != 403 {
		t.Errorf("no CSRF header: %d", res.StatusCode)
	}
	res.Body.Close()
	// the presets and help
	var p map[string]any
	w.call(t, "GET", "/api/v1/presets", nil, &p)
	if p["influx"] == nil || p["defaults"] == nil {
		t.Error("presets")
	}
	w.call(t, "GET", "/api/v1/templates/help", nil, &p)
	if p["variables"] == nil {
		t.Error("help")
	}
	// settings
	var s config.Settings
	w.call(t, "PUT", "/api/v1/settings", map[string]any{"snapshot": false, "recent": 100, "lang": "en"}, &s)
	if s.Snapshot || s.Recent != 100 || s.Lang != "en" {
		t.Errorf("settings: %+v", s)
	}
}

func TestStream(t *testing.T) {
	w := newWorld(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", w.app.URL+"/addons/forwarder/api/v1/stream", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	buf := make([]byte, 4096)
	n, _ := res.Body.Read(buf)
	if !strings.HasPrefix(string(buf[:n]), "event: status\ndata: {") {
		t.Errorf("stream: %q", buf[:n])
	}
}

func TestScrapeEndpointAndMetricGroups(t *testing.T) {
	w := newWorld(t)
	// the inventory documents the metric groups for the selection editor
	var inv InventoryView
	w.call(t, "GET", "/api/v1/inventory", nil, &inv)
	if len(inv.MetricGroups) < 10 || inv.MetricGroups[0].Name != "service_messages" || inv.MetricGroups[0].Title["de"] == "" || len(inv.MetricGroups[0].Metrics) == 0 {
		t.Fatalf("metric groups: %+v", inv.MetricGroups)
	}
	var health Health
	w.call(t, "GET", "/api/v1/health", nil, &health)
	if len(health.Types) != 4 || health.Metrics.Groups == nil {
		t.Errorf("health: %+v", health)
	}
	// a Prometheus scrape target over everything
	var tv TargetView
	body := map[string]any{"name": "prom", "type": "prometheus", "enabled": true, "batch": map[string]any{"size": 10, "interval_ms": 100}, "prometheus": map[string]any{"mode": "scrape", "name": config.PrometheusPresets["clean"].Name, "labels": config.PrometheusPresets["clean"].Labels}, "set_secrets": map[string]string{"scrape_token": "scr4pe"}}
	if code := w.call(t, "POST", "/api/v1/targets", body, &tv); code != 201 {
		t.Fatalf("create target: %d", code)
	}
	w.lite.PushEvent("HmIP-RF", "000A:1", "ACTUAL_TEMPERATURE", 23.5)
	get := func(path, auth string) (int, string) {
		req, _ := http.NewRequest("GET", w.app.URL+"/addons/forwarder"+path, nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(b)
	}
	waitFor(t, func() bool {
		code, text := get("/metrics/"+tv.ID+"?token=scr4pe", "")
		return code == 200 && strings.Contains(text, `homematic_actual_temperature{`) && strings.Contains(text, `} 23.5 `)
	})
	code, text := get("/metrics/"+tv.ID, "Bearer scr4pe")
	if code != 200 || !strings.Contains(text, "# TYPE homematic_actual_temperature gauge") || !strings.Contains(text, `room="Bad"`) || !strings.Contains(text, "forwarder_target_sent_total{target=\"prom\"") || !strings.Contains(text, "forwarder_source_connected 1") {
		t.Errorf("scrape: %d\n%s", code, text)
	}
	if code, _ := get("/metrics/"+tv.ID, ""); code != 401 {
		t.Errorf("no token: %d", code)
	}
	if code, _ := get("/metrics/"+tv.ID, "Bearer wrong"); code != 401 {
		t.Errorf("wrong token: %d", code)
	}
	if code, _ := get("/metrics/nosuch?token=scr4pe", ""); code != 404 {
		t.Errorf("unknown target: %d", code)
	}
	// a disabled target no longer answers
	w.call(t, "POST", "/api/v1/targets/"+tv.ID+"/enabled", map[string]any{"enabled": false}, nil)
	if code, _ := get("/metrics/"+tv.ID+"?token=scr4pe", ""); code != 404 {
		t.Errorf("disabled: %d", code)
	}
	// the presets carry the new types
	var presets map[string]json.RawMessage
	w.call(t, "GET", "/api/v1/presets", nil, &presets)
	for _, k := range []string{"elastic", "elastic_order", "prometheus", "prometheus_order", "defaults"} {
		if _, ok := presets[k]; !ok {
			t.Errorf("presets lack %s", k)
		}
	}
	// the metrics interval is a setting with bounds
	var settings config.Settings
	w.call(t, "PUT", "/api/v1/settings", map[string]any{"snapshot": true, "recent": 100, "lang": "en", "metrics_interval_s": 5}, &settings)
	if settings.MetricsIntervalS != 60 {
		t.Errorf("interval %d", settings.MetricsIntervalS)
	}
}
