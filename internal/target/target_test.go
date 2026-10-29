package target

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

func sample() *record.Record {
	r := record.Event(time.Date(2026, 10, 29, 14, 3, 12, 481000000, time.UTC), "HmIP-RF", "000A1BE9A1B2C3:1", "ACTUAL_TEMPERATURE", json.RawMessage(`21.4`), "FLOAT", nil)
	r.Unit = "°C"
	r.DeviceType = "HmIP-eTRV-2"
	r.ChannelType = "HEATING_CLIMATECONTROL_TRANSCEIVER"
	r.DeviceName = "Heizung Bad"
	r.ChannelName = "Heizung Bad:1"
	r.Rooms = []string{"Bad"}
	r.Functions = []string{"Heizung"}
	r.Source = "event"
	return r
}

func boolRec() *record.Record {
	r := record.Event(time.Unix(0, 1000), "BidCos-RF", "ABC:1", "STATE", json.RawMessage(`true`), "BOOL", nil)
	r.DeviceName = "Lampe, Flur"
	return r
}

func TestInfluxLine(t *testing.T) {
	cfg := config.DefaultTarget("influx")
	cfg.Name = "t"
	cfg.Influx.URL = "http://x"
	cfg.Influx.Org, cfg.Influx.Bucket = "o", "b"
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	in, err := NewInflux(cfg)
	if err != nil {
		t.Fatal(err)
	}
	line, err := in.Line(sample())
	if err != nil {
		t.Fatal(err)
	}
	want := `ACTUAL_TEMPERATURE,interface=HmIP-RF,device=000A1BE9A1B2C3,channel=1,address=000A1BE9A1B2C3:1,device_type=HmIP-eTRV-2,device_name=Heizung\ Bad,channel_name=Heizung\ Bad:1,room=Bad,function=Heizung,unit=°C value=21.4,value_num=21.4 1793282592481000000`
	if line != want {
		t.Errorf("got  %s\nwant %s", line, want)
	}
	line, _ = in.Line(boolRec())
	if want := `STATE,interface=BidCos-RF,device=ABC,channel=1,address=ABC:1,device_name=Lampe\,\ Flur value=true,value_num=1 1000`; line != want {
		t.Errorf("bool: got  %s\nwant %s", line, want)
	}
	// per_device: field per key, string value quoted
	cfg.Influx.Event = config.InfluxPresets["per_device"].Event
	in, _ = NewInflux(cfg)
	r := sample()
	r.SetValue(json.RawMessage(`"a \"b\""`), nil)
	line, _ = in.Line(r)
	if want := `Heizung\ Bad,interface=HmIP-RF,device=000A1BE9A1B2C3,channel=1,channel_name=Heizung\ Bad:1,room=Bad ACTUAL_TEMPERATURE="a \"b\"" 1793282592481000000`; line != want {
		t.Errorf("per_device: got  %s\nwant %s", line, want)
	}
	// typed fields and errors
	cfg.Influx.Event = config.InfluxTemplates{Measurement: "m", Fields: []config.FieldTemplate{{Name: "i", Value: "{{ value }}", Type: "integer"}, {Name: "b", Value: "{{ value > 20 }}", Type: "boolean"}, {Name: "s", Value: "{{ key }}", Type: "string"}, {Name: "empty", Value: "{{ unit if false }}", Type: "float"}}}
	in, _ = NewInflux(cfg)
	line, err = in.Line(sample())
	if err != nil || line != `m i=21i,b=true,s="ACTUAL_TEMPERATURE" 1793282592481000000` {
		t.Errorf("typed: %s %v", line, err)
	}
	cfg.Influx.Event = config.InfluxTemplates{Measurement: "{{ nothing }}", Fields: []config.FieldTemplate{{Name: "v", Value: "{{ value }}", Type: "auto"}}}
	in, _ = NewInflux(cfg)
	if _, err := in.Line(sample()); err == nil {
		t.Error("empty measurement accepted")
	}
	cfg.Influx.Event = config.InfluxTemplates{Measurement: "m", Fields: []config.FieldTemplate{{Name: "v", Value: "{{ key }}", Type: "float"}}}
	in, _ = NewInflux(cfg)
	if _, err := in.Line(sample()); err == nil || !strings.Contains(err.Error(), "not a number") {
		t.Errorf("bad float: %v", err)
	}
	cfg.Influx.Event = config.InfluxTemplates{Measurement: "{{ key | nosuch }}", Fields: []config.FieldTemplate{{Name: "v", Value: "{{ value }}", Type: "auto"}}}
	if _, err := NewInflux(cfg); err == nil {
		t.Error("bad template compiled")
	}
}

type capture struct {
	mu   sync.Mutex
	reqs []*http.Request
	body []string
	code int
}

func (c *capture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var rd io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		rd, _ = gzip.NewReader(r.Body)
	}
	b, _ := io.ReadAll(rd)
	c.reqs = append(c.reqs, r)
	c.body = append(c.body, string(b))
	if c.code != 0 {
		http.Error(w, "nope", c.code)
		return
	}
	w.WriteHeader(204)
}

func TestInfluxSend(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	cfg := config.DefaultTarget("influx")
	cfg.Name = "t"
	cfg.Influx.URL = srv.URL + "/"
	cfg.Influx.Org, cfg.Influx.Bucket = "o", "b"
	cfg.Secrets = map[string]string{"token": "secret"}
	_ = cfg.Validate()
	in, _ := NewInflux(cfg)
	payloads, errs := in.Encode([]*record.Record{sample(), boolRec()})
	if len(payloads) != 1 || payloads[0].Records != 2 || len(errs) != 0 {
		t.Fatalf("encode: %+v %v", payloads, errs)
	}
	if err := in.Send(context.Background(), payloads[0]); err != nil {
		t.Fatal(err)
	}
	req := c.reqs[0]
	if req.URL.Path != "/api/v2/write" || req.URL.Query().Get("bucket") != "b" || req.Header.Get("Authorization") != "Token secret" || req.Header.Get("Content-Encoding") != "gzip" {
		t.Errorf("request: %s %s %v", req.URL, req.Header.Get("Authorization"), req.Header)
	}
	if strings.Count(c.body[0], "\n") != 2 {
		t.Errorf("body: %q", c.body[0])
	}
	// v1 with basic auth, no gzip
	cfg.Influx.Version, cfg.Influx.Database, cfg.Influx.RetentionPolicy, cfg.Influx.User, cfg.Influx.Gzip = "1", "db", "rp", "u", false
	cfg.Secrets = map[string]string{"password": "p"}
	in, _ = NewInflux(cfg)
	if _, err := in.Test(context.Background(), Sample()); err != nil {
		t.Fatal(err)
	}
	req = c.reqs[1]
	u, p, _ := req.BasicAuth()
	if req.URL.Path != "/write" || req.URL.Query().Get("db") != "db" || req.URL.Query().Get("rp") != "rp" || u != "u" || p != "p" || !strings.HasPrefix(c.body[1], "FORWARDER_TEST,") {
		t.Errorf("v1 request: %s %s/%s %q", req.URL, u, p, c.body[1])
	}
	// errors: 400 permanent, 500 retryable, 401 retryable
	for code, perm := range map[int]bool{400: true, 500: false, 401: false, 404: false, 422: true} {
		c.code = code
		err := in.Send(context.Background(), payloads[0])
		if err == nil || IsPermanent(err) != perm {
			t.Errorf("%d: %v permanent=%v", code, err, IsPermanent(err))
		}
	}
	c.code = 0
	// unreachable: a port nothing listens on
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().String()
	l.Close()
	cfg.Influx.URL = "http://" + closed
	in, _ = NewInflux(cfg)
	if err := in.Send(context.Background(), payloads[0]); err == nil || IsPermanent(err) {
		t.Errorf("unreachable: %v", err)
	}
}

func TestWebhook(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	cfg := config.DefaultTarget("webhook")
	cfg.Name = "t"
	cfg.Webhook.URL = srv.URL + "/hook/{{ kind }}"
	cfg.Webhook.Auth = "bearer"
	cfg.Webhook.Headers = map[string]string{"X-Test": "1"}
	cfg.Secrets = map[string]string{"token": "tok"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	w, err := NewWebhook(cfg)
	if err != nil {
		t.Fatal(err)
	}
	payloads, errs := w.Encode([]*record.Record{sample(), boolRec()})
	if len(payloads) != 1 || len(errs) != 0 || payloads[0].Records != 2 {
		t.Fatalf("%+v %v", payloads, errs)
	}
	var arr []map[string]any
	if err := json.Unmarshal(payloads[0].Body, &arr); err != nil || len(arr) != 2 || arr[0]["key"] != "ACTUAL_TEMPERATURE" || arr[0]["device_name"] != "Heizung Bad" {
		t.Errorf("json: %s %v", payloads[0].Body, err)
	}
	if err := w.Send(context.Background(), payloads[0]); err != nil {
		t.Fatal(err)
	}
	req := c.reqs[0]
	if req.URL.Path != "/hook/event" || req.Header.Get("Authorization") != "Bearer tok" || req.Header.Get("X-Test") != "1" || req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request: %s %v", req.URL, req.Header)
	}
	// ndjson with a custom body, grouped by URL
	cfg.Webhook.Format = "ndjson"
	cfg.Webhook.URL = srv.URL + "/{{ interface }}"
	cfg.Webhook.Body = `{"k":"{{ key }}","v":{{ value | tojson }}}`
	cfg.Webhook.Auth = "basic"
	cfg.Webhook.User = "me"
	cfg.Secrets = map[string]string{"password": "pw"}
	w, _ = NewWebhook(cfg)
	payloads, errs = w.Encode([]*record.Record{sample(), boolRec(), sample()})
	if len(payloads) != 2 || len(errs) != 0 || payloads[0].Records != 2 || !strings.HasSuffix(payloads[0].URL, "/HmIP-RF") {
		t.Fatalf("ndjson: %+v %v", payloads, errs)
	}
	if string(payloads[0].Body) != "{\"k\":\"ACTUAL_TEMPERATURE\",\"v\":21.4}\n{\"k\":\"ACTUAL_TEMPERATURE\",\"v\":21.4}\n" {
		t.Errorf("ndjson body: %q", payloads[0].Body)
	}
	out, err := w.Test(context.Background(), Sample())
	if err != nil || !strings.HasPrefix(out, "HTTP 204") {
		t.Errorf("test: %q %v", out, err)
	}
	u, p, _ := c.reqs[1].BasicAuth()
	if u != "me" || p != "pw" || c.reqs[1].Header.Get("Content-Type") != "application/x-ndjson" {
		t.Errorf("basic: %s %s", u, p)
	}
	// a body that is not JSON in json format is an error per record
	cfg.Webhook.Format = "json"
	cfg.Webhook.Body = `{{ key }}`
	w, _ = NewWebhook(cfg)
	if payloads, errs = w.Encode([]*record.Record{sample()}); len(payloads) != 0 || len(errs) != 1 {
		t.Errorf("not json: %+v %v", payloads, errs)
	}
}

func TestClientTLS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer srv.Close()
	cfg := config.DefaultTarget("webhook")
	cfg.Name = "t"
	cfg.Webhook.URL = srv.URL
	_ = cfg.Validate()
	w, _ := NewWebhook(cfg)
	if err := w.Send(context.Background(), Payload{URL: srv.URL, Body: []byte("[]"), ContentType: "application/json"}); err == nil {
		t.Error("unknown CA accepted")
	}
	cfg.TLS.Insecure = true
	w, _ = NewWebhook(cfg)
	if err := w.Send(context.Background(), Payload{URL: srv.URL, Body: []byte("[]"), ContentType: "application/json"}); err != nil {
		t.Errorf("insecure: %v", err)
	}
	cfg.TLS.Insecure = false
	cfg.TLS.CA = "garbage"
	if _, err := NewWebhook(cfg); err == nil {
		t.Error("bad CA accepted")
	}
}
