// Package roundtrip proves the targets against real stores in docker: a record goes in through
// the target and is read back through the store's own query API. FORWARDER_DOCKER=1 runs it
// (CI does; locally it needs docker and pulls influxdb:1.8 and influxdb:2.7).
package roundtrip

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/target"
)

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// container runs an image with a fixed name (removed first and at the end) and answers its URL.
func container(t *testing.T, name, image string, port, inner int, env ...string) string {
	t.Helper()
	_ = exec.Command("docker", "rm", "-f", name).Run()
	args := []string{"run", "-d", "--rm", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:%d", port, inner)}
	for _, e := range env {
		args = append(args, "-e", e)
	}
	args = append(args, image)
	docker(t, args...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func waitHTTP(t *testing.T, u string, ok func(*http.Response) bool) {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(u)
		if err == nil {
			good := ok(res)
			res.Body.Close()
			if good {
				return
			}
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("%s did not come up", u)
}

func sample(ts time.Time) *record.Record {
	r := record.Event(ts, "HmIP-RF", "000A1BE9A1B2C3:1", "ACTUAL_TEMPERATURE", json.RawMessage(`21.4`), "FLOAT", nil)
	r.Unit = "°C"
	r.DeviceType = "HmIP-eTRV-2"
	r.DeviceName = "Heizung Bad"
	r.ChannelName = "Heizung Bad:1"
	r.Rooms = []string{"Bad"}
	r.Functions = []string{"Heizung"}
	r.Source = "event"
	return r
}

func boolSample(ts time.Time) *record.Record {
	r := record.Event(ts, "BidCos-RF", "MEQ0123456:1", "STATE", json.RawMessage(`true`), "BOOL", nil)
	r.DeviceName = "Lampe Flur"
	r.ChannelName = "Lampe Flur:1"
	r.Source = "event"
	return r
}

func TestRoundTrip(t *testing.T) {
	if os.Getenv("FORWARDER_DOCKER") == "" {
		t.Skip("FORWARDER_DOCKER not set")
	}
	t.Run("influx1", testInflux1)
	t.Run("influx2", testInflux2)
}

func testInflux1(t *testing.T) {
	port := freePort(t)
	base := container(t, "forwarder-rt-influx1", "influxdb:1.8", port, 8086, "INFLUXDB_DB=hm", "INFLUXDB_HTTP_AUTH_ENABLED=true", "INFLUXDB_ADMIN_USER=admin", "INFLUXDB_ADMIN_PASSWORD=adminpw")
	waitHTTP(t, base+"/ping", func(r *http.Response) bool { return r.StatusCode == 204 })

	cfg := config.DefaultTarget("influx")
	cfg.Name = "rt1"
	cfg.Influx.Version = "1"
	cfg.Influx.URL = base
	cfg.Influx.Database = "hm"
	cfg.Influx.User = "admin"
	cfg.Secrets = map[string]string{"password": "adminpw"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	tgt, err := target.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	payloads, errs := tgt.Encode([]*record.Record{sample(ts), boolSample(ts.Add(time.Second))})
	if len(errs) > 0 || len(payloads) != 1 {
		t.Fatalf("encode: %v %d", errs, len(payloads))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := tgt.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}
	if msg, err := tgt.Test(ctx, target.Sample()); err != nil || !strings.HasPrefix(msg, "HTTP 204") {
		t.Fatalf("test: %q %v", msg, err)
	}

	// read back with InfluxQL
	q := url.Values{"db": {"hm"}, "q": {`SELECT "value","value_num","device_name","room" FROM "ACTUAL_TEMPERATURE"`}, "epoch": {"ns"}}
	req, _ := http.NewRequest("GET", base+"/query?"+q.Encode(), nil)
	req.SetBasicAuth("admin", "adminpw")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var out struct {
		Results []struct {
			Series []struct {
				Columns []string `json:"columns"`
				Values  [][]any  `json:"values"`
			} `json:"series"`
		} `json:"results"`
	}
	if err := json.Unmarshal(b, &out); err != nil || len(out.Results) == 0 || len(out.Results[0].Series) == 0 {
		t.Fatalf("query: %s %v", b, err)
	}
	s := out.Results[0].Series[0]
	t.Logf("influx 1.8 answered %v %v", s.Columns, s.Values)
	if len(s.Values) != 1 {
		t.Fatalf("rows: %v", s.Values)
	}
	row := s.Values[0]
	if fmt.Sprint(row[0]) != fmt.Sprint(float64(ts.UnixNano())) && fmt.Sprint(row[0]) != fmt.Sprint(ts.UnixNano()) {
		t.Errorf("time: %v want %d", row[0], ts.UnixNano())
	}
	if row[1] != 21.4 || row[2] != 21.4 || row[3] != "Heizung Bad" || row[4] != "Bad" {
		t.Errorf("row: %v", row)
	}
	// the bool went in as a boolean field
	q.Set("q", `SELECT "value","value_num" FROM "STATE"`)
	req, _ = http.NewRequest("GET", base+"/query?"+q.Encode(), nil)
	req.SetBasicAuth("admin", "adminpw")
	res, _ = http.DefaultClient.Do(req)
	b, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.Contains(string(b), `,true,1]`) {
		t.Errorf("bool row: %s", b)
	}
	// a wrong password is retried (401), not dropped
	cfg.Secrets["password"] = "wrong"
	tgt, _ = target.New(cfg)
	if err := tgt.Send(ctx, payloads[0]); err == nil || target.IsPermanent(err) || !strings.Contains(err.Error(), "401") {
		t.Errorf("wrong password: %v", err)
	}
}

func testInflux2(t *testing.T) {
	port := freePort(t)
	base := container(t, "forwarder-rt-influx2", "influxdb:2.7", port, 8086,
		"DOCKER_INFLUXDB_INIT_MODE=setup", "DOCKER_INFLUXDB_INIT_USERNAME=admin", "DOCKER_INFLUXDB_INIT_PASSWORD=adminpw123",
		"DOCKER_INFLUXDB_INIT_ORG=home", "DOCKER_INFLUXDB_INIT_BUCKET=hm", "DOCKER_INFLUXDB_INIT_ADMIN_TOKEN=rt-token")
	waitHTTP(t, base+"/health", func(r *http.Response) bool { return r.StatusCode == 200 })
	// the setup takes a moment after /health answers
	time.Sleep(2 * time.Second)

	cfg := config.DefaultTarget("influx")
	cfg.Name = "rt2"
	cfg.Influx.URL = base + "/"
	cfg.Influx.Org, cfg.Influx.Bucket = "home", "hm"
	cfg.Influx.Event = config.InfluxPresets["per_device"].Event
	cfg.Secrets = map[string]string{"token": "rt-token"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	tgt, err := target.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ts := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	payloads, errs := tgt.Encode([]*record.Record{sample(ts), boolSample(ts)})
	if len(errs) > 0 || len(payloads) != 1 {
		t.Fatalf("encode: %v %d", errs, len(payloads))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := tgt.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}

	// read back with Flux
	flux := `from(bucket:"hm") |> range(start: 2026-10-29T00:00:00Z, stop: 2030-01-01T00:00:00Z) |> keep(columns: ["_time","_measurement","_field","_value","room","channel"])`
	req, _ := http.NewRequest("POST", base+"/api/v2/query?org=home", strings.NewReader(flux))
	req.Header.Set("Authorization", "Token rt-token")
	req.Header.Set("Content-Type", "application/vnd.flux")
	req.Header.Set("Accept", "application/csv")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	csv := string(b)
	t.Logf("influx 2.7 answered:\n%s", csv)
	if res.StatusCode != 200 || !strings.Contains(csv, "2026-10-29T12:00:00Z,21.4,ACTUAL_TEMPERATURE,Heizung Bad,1,Bad") || !strings.Contains(csv, "2026-10-29T12:00:00Z,true,STATE,Lampe Flur,1") {
		t.Errorf("flux: %d %s", res.StatusCode, csv)
	}
	// an unknown bucket is retried (404), a bad line is permanent (400)
	cfg.Influx.Bucket = "nope"
	tgt, _ = target.New(cfg)
	if err := tgt.Send(ctx, payloads[0]); err == nil || target.IsPermanent(err) || !strings.Contains(err.Error(), "404") {
		t.Errorf("unknown bucket: %v", err)
	}
	cfg.Influx.Bucket = "hm"
	tgt, _ = target.New(cfg)
	if err := tgt.Send(ctx, target.Payload{Body: []byte("this is not line protocol\n"), Records: 1}); err == nil || !target.IsPermanent(err) {
		t.Errorf("bad line: %v", err)
	}
}
