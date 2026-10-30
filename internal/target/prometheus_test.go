package target

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

func TestPrometheusSample(t *testing.T) {
	cfg := config.DefaultTarget("prometheus")
	cfg.Name = "p"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p, err := NewPrometheus(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := p.Sample(sample())
	if err != nil {
		t.Fatal(err)
	}
	if got := exposeLine(s); got != `homematic_actual_temperature{address="000A1BE9A1B2C3:1",channel_name="Heizung Bad:1",device_name="Heizung Bad",device_type="HmIP-eTRV-2",function="Heizung",interface="HmIP-RF",room="Bad"} 21.4` {
		t.Errorf("event: %s", got)
	}
	// a metric: group_name, every label (the "*" label), no event labels (they render empty)
	m := record.Metric(time.UnixMilli(1700000000000), "cpu", "usage_percent", 12.5, map[string]string{"mode": "total", "host": "ccu"})
	s, err = p.Sample(m)
	if err != nil {
		t.Fatal(err)
	}
	if got := exposeLine(s); got != `cpu_usage_percent{host="ccu",mode="total"} 12.5` {
		t.Errorf("metric: %s", got)
	}
	// a bool is 1, a string has no number
	s, _ = p.Sample(boolRec())
	if s.Value != 1 || s.Name != "homematic_state" {
		t.Errorf("bool: %+v", s)
	}
	r := sample()
	r.SetValue(json.RawMessage(`"text"`), nil)
	if _, err := p.Sample(r); err == nil || !strings.Contains(err.Error(), "no numeric value") {
		t.Errorf("string: %v", err)
	}
	// names and labels are sanitised, a __ label refused
	for in, want := range map[string]string{"homematic_ACTUAL-TEMP.2": "homematic_ACTUAL_TEMP_2", "1abc": "_1abc", "a:b": "a:b", " ": ""} {
		if got := MetricName(in); got != want {
			t.Errorf("MetricName(%q) = %q", in, got)
		}
	}
	if LabelName("__x") != "" || LabelName("a:b") != "a_b" || LabelName("room-1") != "room_1" {
		t.Error("label names")
	}
	cfg.Prometheus.Labels = []config.NamedTemplate{{Name: "__reserved", Value: "x"}, {Name: "ok", Value: "{{ key }}"}, {Name: "empty", Value: "{{ unit if false }}"}}
	p, _ = NewPrometheus(cfg)
	s, _ = p.Sample(sample())
	if len(s.Labels) != 1 || s.Labels["ok"] != "ACTUAL_TEMPERATURE" {
		t.Errorf("labels: %v", s.Labels)
	}
	// a bad template does not compile
	cfg.Prometheus.Name = "{{ key | nosuch }}"
	if _, err := NewPrometheus(cfg); err == nil {
		t.Error("bad template compiled")
	}
}

func TestPrometheusScrape(t *testing.T) {
	cfg := config.DefaultTarget("prometheus")
	cfg.Name = "p"
	cfg.Prometheus.StaleS = 3600
	_ = cfg.Validate()
	p, _ := NewPrometheus(cfg)
	old := sample()
	old.TS = time.Now().Add(-2 * time.Hour)
	fresh := sample()
	fresh.TS = time.Now()
	fresh.SetValue(json.RawMessage(`22.0`), nil)
	m := record.Metric(time.Now(), "cpu", "load", 0.7, map[string]string{"period": "1"})
	c := record.Metric(time.Now(), "network", "rx_bytes_total", 5, map[string]string{"interface": "eth0"})
	payloads, errs := p.Encode([]*record.Record{fresh, old, m, c})
	if len(payloads) != 1 || payloads[0].Records != 4 || len(errs) != 0 || payloads[0].ContentType != "application/json" {
		t.Fatalf("encode %+v %v", payloads, errs)
	}
	if err := p.Send(context.Background(), payloads[0]); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	p.Expose(&buf, time.Now())
	out := buf.String()
	// the newer sample of the series won although it came first; the stale one is gone
	if !strings.Contains(out, "# TYPE homematic_actual_temperature gauge\n") || !strings.Contains(out, `} 22 `) || strings.Contains(out, `} 21.4 `) {
		t.Errorf("exposition:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE network_rx_bytes_total counter\n") || !strings.Contains(out, `cpu_load{period="1"} 0.7 `) {
		t.Errorf("exposition:\n%s", out)
	}
	if msg, err := p.Test(context.Background(), Sample()); err != nil || !strings.Contains(msg, "homematic_forwarder_test") {
		t.Errorf("test: %q %v", msg, err)
	}
	text, _ := p.Preview([]*record.Record{m})
	if !strings.HasPrefix(text, "cpu_load{") {
		t.Errorf("preview %q", text)
	}
}

// decodeWriteRequest reads the protobuf back: series -> labels and samples.
type decodedSeries struct {
	labels  map[string]string
	samples [][2]float64
}

func decodeWriteRequest(t *testing.T, b []byte) []decodedSeries {
	t.Helper()
	var out []decodedSeries
	for len(b) > 0 {
		tag, n := binary.Uvarint(b)
		b = b[n:]
		if tag != 1<<3|2 {
			t.Fatalf("unexpected tag %d", tag)
		}
		l, n := binary.Uvarint(b)
		b = b[n:]
		ts := b[:l]
		b = b[l:]
		ds := decodedSeries{labels: map[string]string{}}
		for len(ts) > 0 {
			tag, n := binary.Uvarint(ts)
			ts = ts[n:]
			l, n := binary.Uvarint(ts)
			ts = ts[n:]
			msg := ts[:l]
			ts = ts[l:]
			switch tag >> 3 {
			case 1: // label
				var name, value string
				for len(msg) > 0 {
					ftag, n := binary.Uvarint(msg)
					msg = msg[n:]
					fl, n := binary.Uvarint(msg)
					msg = msg[n:]
					if ftag>>3 == 1 {
						name = string(msg[:fl])
					} else {
						value = string(msg[:fl])
					}
					msg = msg[fl:]
				}
				ds.labels[name] = value
			case 2: // sample
				var v float64
				var tsv int64
				for len(msg) > 0 {
					ftag := msg[0]
					msg = msg[1:]
					if ftag == 0x09 {
						v = math.Float64frombits(binary.LittleEndian.Uint64(msg[:8]))
						msg = msg[8:]
					} else {
						x, n := binary.Uvarint(msg)
						tsv = int64(x)
						msg = msg[n:]
					}
				}
				ds.samples = append(ds.samples, [2]float64{v, float64(tsv)})
			}
		}
		out = append(out, ds)
	}
	return out
}

func TestPrometheusRemoteWrite(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	cfg := config.DefaultTarget("prometheus")
	cfg.Name = "p"
	cfg.Prometheus.Mode = "remote_write"
	cfg.Prometheus.URL = srv.URL + "/api/v1/write"
	cfg.Prometheus.Auth = "bearer"
	cfg.Secrets = map[string]string{"token": "tok"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	p, err := NewPrometheus(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := sample()
	b := sample()
	b.TS = a.TS.Add(time.Second)
	b.SetValue(json.RawMessage(`21.5`), nil)
	m := record.Metric(time.UnixMilli(1700000000000), "cpu", "load", 0.7, map[string]string{"period": "1", "host": "ccu"})
	payloads, errs := p.Encode([]*record.Record{b, m, a})
	if len(payloads) != 1 || payloads[0].Records != 3 || len(errs) != 0 || payloads[0].ContentType != "application/x-protobuf" {
		t.Fatalf("encode %+v %v", payloads, errs)
	}
	raw, err := snappy.Decode(nil, payloads[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	series := decodeWriteRequest(t, raw)
	if len(series) != 2 {
		t.Fatalf("series %d", len(series))
	}
	// the two temperature samples in one series, in time order; the labels sorted with __name__ first
	if series[0].labels["__name__"] != "homematic_actual_temperature" || series[0].labels["room"] != "Bad" || len(series[0].samples) != 2 || series[0].samples[0][0] != 21.4 || series[0].samples[1][0] != 21.5 || series[0].samples[0][1] != float64(a.TS.UnixMilli()) {
		t.Errorf("series 0: %+v", series[0])
	}
	if series[1].labels["__name__"] != "cpu_load" || series[1].labels["period"] != "1" || series[1].samples[0][0] != 0.7 || series[1].samples[0][1] != 1700000000000 {
		t.Errorf("series 1: %+v", series[1])
	}
	if err := p.Send(context.Background(), payloads[0]); err != nil {
		t.Fatal(err)
	}
	req := c.reqs[0]
	if req.URL.Path != "/api/v1/write" || req.Header.Get("Content-Encoding") != "snappy" || req.Header.Get("Content-Type") != "application/x-protobuf" || req.Header.Get("X-Prometheus-Remote-Write-Version") != "0.1.0" || req.Header.Get("Authorization") != "Bearer tok" {
		t.Errorf("request %v", req.Header)
	}
	if msg, err := p.Test(context.Background(), Sample()); err != nil || !strings.Contains(msg, "HTTP 204") {
		t.Errorf("test: %q %v", msg, err)
	}
	c.code = 400
	if err := p.Send(context.Background(), payloads[0]); !IsPermanent(err) {
		t.Errorf("400: %v", err)
	}
	c.code = 500
	if err := p.Send(context.Background(), payloads[0]); err == nil || IsPermanent(err) {
		t.Errorf("500: %v", err)
	}
	_ = http.StatusOK
}
