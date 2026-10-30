package roundtrip

import (
	"context"
	"database/sql"
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

// TestRoundTripStores03 proves the 0.3.0 targets against PostgreSQL 16, TimescaleDB, MariaDB 11,
// Grafana Loki 3 and rsyslog in docker. FORWARDER_DOCKER=1 runs it; FORWARDER_DOCKER_STORES
// narrows it (postgres, timescale, mariadb, loki, rsyslog).
func TestRoundTripStores03(t *testing.T) {
	if os.Getenv("FORWARDER_DOCKER") == "" {
		t.Skip("FORWARDER_DOCKER not set")
	}
	only := os.Getenv("FORWARDER_DOCKER_STORES")
	run := func(name string, f func(*testing.T)) {
		if only == "" || strings.Contains(","+only+",", ","+name+",") {
			t.Run(name, f)
		}
	}
	run("postgres", func(t *testing.T) {
		port := freePort(t)
		container(t, "forwarder-rt-postgres", "postgres:16-alpine", port, 5432, "POSTGRES_PASSWORD=pw", "POSTGRES_DB=hm", "POSTGRES_USER=hm")
		testSQL(t, "postgres", port, "wide")
	})
	run("timescale", func(t *testing.T) {
		port := freePort(t)
		container(t, "forwarder-rt-timescale", "timescale/timescaledb:latest-pg16", port, 5432, "POSTGRES_PASSWORD=pw", "POSTGRES_DB=hm", "POSTGRES_USER=hm")
		testSQL(t, "postgres", port, "timescale")
	})
	run("mariadb", func(t *testing.T) {
		port := freePort(t)
		container(t, "forwarder-rt-mariadb", "mariadb:11", port, 3306, "MARIADB_ROOT_PASSWORD=pw", "MARIADB_DATABASE=hm", "MARIADB_USER=hm", "MARIADB_PASSWORD=pw")
		testSQL(t, "mysql", port, "wide")
	})
	run("loki", testLoki)
	run("rsyslog", testRsyslog)
}

func logSample(ts time.Time) *record.Record {
	return record.Log(ts, "audit", "occulited.service", "info", "api: change", map[string]string{"user": "admin", "remote": "192.0.2.10", "route": "/api/system/v1/network"})
}

// testSQL writes the three kinds with a preset into a fresh database and reads them back.
func testSQL(t *testing.T, dialect string, port int, preset string) {
	cfg := config.DefaultTarget("sql")
	cfg.Name = "rt-" + dialect
	cfg.SQL.Dialect, cfg.SQL.Host, cfg.SQL.Port, cfg.SQL.Database, cfg.SQL.User = dialect, "127.0.0.1", port, "hm", "hm"
	p := config.SQLPresets[preset]
	st := p.Postgres
	if dialect == "mysql" {
		st = p.MySQL
	}
	cfg.SQL.Insert, cfg.SQL.Schema, cfg.SQL.InstallSchema, cfg.SQL.Preset = st.Insert, st.Schema, true, preset
	cfg.Secrets = map[string]string{"password": "pw"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	tgt, err := target.NewSQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer tgt.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	// the server takes a while: the Test is retried until it answers
	var msg string
	deadline := time.Now().Add(120 * time.Second)
	for {
		msg, err = tgt.Test(ctx, target.Sample())
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		t.Fatalf("test: %v", err)
	}
	t.Logf("%s test answered:\n%s", dialect, msg)
	if !strings.Contains(msg, "INSERT OK (1 row), rolled back") || !strings.Contains(msg, "schema statements") {
		t.Errorf("test message: %q", msg)
	}
	ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	payloads, errs := tgt.Encode([]*record.Record{sample(ts), boolSample(ts.Add(time.Second)), metricSample(ts), logSample(ts)})
	if len(errs) != 0 || len(payloads) != 1 || payloads[0].Records != 4 {
		t.Fatalf("encode: %v %d", errs, len(payloads))
	}
	if err := tgt.Send(ctx, payloads[0]); err != nil {
		t.Fatalf("send: %v", err)
	}
	// read back through the driver
	var db *sql.DB
	if dialect == "postgres" {
		db, err = sql.Open("pgx", fmt.Sprintf("postgres://hm:pw@127.0.0.1:%d/hm?sslmode=disable", port))
	} else {
		db, err = sql.Open("mysql", fmt.Sprintf("hm:pw@tcp(127.0.0.1:%d)/hm?parseTime=true", port))
	}
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	q := "SELECT kind, address, key, value_num, value_text, value_bool, device_name, rooms, metric_name, labels, stream, message, fields FROM homematic ORDER BY ts, kind"
	if dialect == "mysql" {
		q = strings.Replace(q, " key,", " `key`,", 1)
	}
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var kind, address, key, valueText, deviceName, rooms, metricName, labels, stream, message, fields sql.NullString
		var valueNum sql.NullFloat64
		var valueBool sql.NullBool
		if err := rows.Scan(&kind, &address, &key, &valueNum, &valueText, &valueBool, &deviceName, &rooms, &metricName, &labels, &stream, &message, &fields); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%s|%s|%s|%v|%s|%v|%s|%s|%s|%s|%s|%s|%s", kind.String, address.String, key.String, valueNum.Float64, valueText.String, valueBool.Bool, deviceName.String, compactJSON(rooms.String), metricName.String, compactJSON(labels.String), stream.String, message.String, compactJSON(fields.String)))
	}
	t.Logf("%s rows:\n%s", dialect, strings.Join(got, "\n"))
	if len(got) != 4 {
		t.Fatalf("rows: %d (the Test's row must have been rolled back)", len(got))
	}
	want := []string{
		`event|000A1BE9A1B2C3:1|ACTUAL_TEMPERATURE|21.4||false|Heizung Bad|["Bad"]||{}|||{}`,
		`log|||0|api: change|false||[]||{}|audit|api: change|{"remote":"192.0.2.10","route":"/api/system/v1/network","user":"admin"}`,
		`metric|||0.75||false||[]|load|{"host":"lab","period":"1"}|||{}`,
		`event|MEQ0123456:1|STATE|1||true|Lampe Flur|[]||{}|||{}`,
	}
	for i, w := range want {
		if i < len(got) && got[i] != w {
			t.Errorf("row %d:\n got %s\nwant %s", i, got[i], w)
		}
	}
	if preset == "timescale" {
		var n int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM timescaledb_information.hypertables WHERE hypertable_name = 'homematic'").Scan(&n); err != nil || n != 1 {
			t.Errorf("hypertable: %d %v", n, err)
		}
	}
	// a statement the server refuses is permanent; a wrong password is retried
	bad := cfg
	bad.SQL.Insert = "INSERT INTO nosuch (a) VALUES (:key)"
	bad.SQL.InstallSchema = false
	bt, err := target.NewSQL(bad)
	if err != nil {
		t.Fatal(err)
	}
	defer bt.Close()
	pl, _ := bt.Encode([]*record.Record{sample(ts)})
	if err := bt.Send(ctx, pl[0]); err == nil || !target.IsPermanent(err) {
		t.Errorf("bad table: %v", err)
	}
	wrong := cfg
	wrong.Secrets = map[string]string{"password": "nope"}
	wt, _ := target.NewSQL(wrong)
	defer wt.Close()
	if err := wt.Send(ctx, payloads[0]); err == nil || target.IsPermanent(err) {
		t.Errorf("wrong password: %v", err)
	} else {
		t.Logf("wrong password answered: %v", err)
	}
}

func compactJSON(s string) string {
	if s == "" {
		return ""
	}
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func testLoki(t *testing.T) {
	port := freePort(t)
	base := container(t, "forwarder-rt-loki", "grafana/loki:3.4.2", port, 3100)
	waitHTTP(t, base+"/ready", func(r *http.Response) bool { return r.StatusCode == 200 })
	cfg := config.DefaultTarget("loki")
	cfg.Name = "rt-loki"
	cfg.Loki.URL = base
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	tgt, err := target.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	ts := time.Now().Add(-time.Minute).Truncate(time.Second)
	payloads, errs := tgt.Encode([]*record.Record{sample(ts), metricSample(ts.Add(time.Second)), logSample(ts.Add(2 * time.Second))})
	if len(errs) != 0 || len(payloads) != 1 {
		t.Fatalf("encode: %v %d", errs, len(payloads))
	}
	if err := tgt.Send(ctx, payloads[0]); err != nil {
		t.Fatalf("send: %v", err)
	}
	if msg, err := tgt.Test(ctx, target.Sample()); err != nil || !strings.HasPrefix(msg, "HTTP 204") {
		t.Fatalf("test: %q %v", msg, err)
	}
	// read back: the streams of the job
	q := url.Values{"query": {`{job="homematic"}`}, "start": {fmt.Sprint(ts.Add(-time.Hour).UnixNano())}, "end": {fmt.Sprint(time.Now().Add(time.Hour).UnixNano())}, "limit": {"100"}}
	var body string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(base + "/loki/api/v1/query_range?" + q.Encode())
		if err == nil {
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			body = string(b)
			if strings.Contains(body, "api: change") && strings.Contains(body, "ACTUAL_TEMPERATURE") {
				break
			}
		}
		time.Sleep(time.Second)
	}
	t.Logf("loki answered: %s", target.Line(body))
	var out struct {
		Data struct {
			Result []struct {
				Stream map[string]string `json:"stream"`
				Values [][]string        `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || len(out.Data.Result) < 3 {
		t.Fatalf("query: %v %s", err, body)
	}
	found := map[string]bool{}
	for _, r := range out.Data.Result {
		for _, v := range r.Values {
			switch {
			case strings.Contains(v[1], "key=ACTUAL_TEMPERATURE value=21.4"):
				found["event"] = r.Stream["kind"] == "event" && r.Stream["interface"] == "HmIP-RF"
			case strings.Contains(v[1], "name=load value=0.75"):
				found["metric"] = r.Stream["group"] == "cpu"
			case v[1] == "api: change":
				found["log"] = r.Stream["stream"] == "audit" && r.Stream["unit"] == "occulited.service"
			}
		}
	}
	if !found["event"] || !found["metric"] || !found["log"] {
		t.Errorf("found %v in %s", found, body)
	}
	// a malformed push is permanent
	if err := tgt.Send(ctx, target.Payload{Body: []byte(`{"streams":[{"stream":{"a":"b"},"values":[["x","y"]]}]}`), Records: 1}); err == nil || !target.IsPermanent(err) {
		t.Errorf("malformed: %v", err)
	}
}

// testRsyslog runs rsyslog in an alpine container, listening on UDP and TCP, writing every
// message raw and parsed into files, and reads them back with docker exec.
func testRsyslog(t *testing.T) {
	udpPort, tcpPort := freePort(t), freePort(t)
	conf := `module(load="imudp")
module(load="imtcp")
input(type="imudp" port="514")
input(type="imtcp" port="514")
template(name="raw" type="string" string="%rawmsg%\n")
template(name="parsed" type="string" string="%app-name%|%msgid%|%syslogfacility-text%|%syslogseverity-text%|%hostname%|%structured-data%|%msg%\n")
*.* action(type="omfile" file="/var/log/raw.log" template="raw")
*.* action(type="omfile" file="/var/log/parsed.log" template="parsed")
`
	name := "forwarder-rt-rsyslog"
	_ = exec.Command("docker", "rm", "-f", name).Run()
	docker(t, "run", "-d", "--rm", "--name", name, "-p", fmt.Sprintf("127.0.0.1:%d:514/udp", udpPort), "-p", fmt.Sprintf("127.0.0.1:%d:514", tcpPort), "-e", "CONF="+conf, "alpine:3.20", "sh", "-c", `apk add -q rsyslog && printf '%s' "$CONF" > /etc/rsyslog.conf && exec rsyslogd -n -f /etc/rsyslog.conf`)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	// wait for rsyslog itself (docker publishes the ports before apk has installed it, so a
	// connect alone proves nothing)
	deadline := time.Now().Add(120 * time.Second)
	for {
		out, _ := exec.Command("docker", "logs", name).CombinedOutput()
		if strings.Contains(string(out), "running as pid 1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("rsyslog did not come up: %s", out)
		}
		time.Sleep(time.Second)
	}
	time.Sleep(time.Second)
	if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", tcpPort), 5*time.Second); err == nil {
		c.Close()
	}
	ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, tr := range []struct{ transport, framing string }{{"udp", ""}, {"tcp", "octet"}, {"tcp", "newline"}} {
		cfg := config.DefaultTarget("syslog")
		cfg.Name = "rt-" + tr.transport + tr.framing
		cfg.Syslog.Host, cfg.Syslog.Transport, cfg.Syslog.Framing, cfg.Syslog.Hostname = "127.0.0.1", tr.transport, tr.framing, "ccu-lab"
		cfg.Syslog.Port = udpPort
		if tr.transport == "tcp" {
			cfg.Syslog.Port = tcpPort
		}
		cfg.Syslog.Message = "{{ kind }} via " + tr.transport + tr.framing + ": {% if kind == 'event' %}{{ key }}={{ value }}{% else %}{{ message }}{% endif %}"
		if err := cfg.Validate(); err != nil {
			t.Fatal(err)
		}
		tgt, err := target.New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		payloads, errs := tgt.Encode([]*record.Record{sample(ts), logSample(ts.Add(time.Second))})
		if len(errs) != 0 || len(payloads) != 1 {
			t.Fatalf("encode: %v %d", errs, len(payloads))
		}
		if err := tgt.Send(ctx, payloads[0]); err != nil {
			t.Fatalf("send %s: %v", tr.transport, err)
		}
	}
	time.Sleep(2 * time.Second)
	var raw, parsed string
	deadline = time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		raw = docker(t, "exec", name, "cat", "/var/log/raw.log")
		if strings.Count(raw, "\n") >= 5 {
			break
		}
		time.Sleep(time.Second)
	}
	parsed = docker(t, "exec", name, "cat", "/var/log/parsed.log")
	t.Logf("rsyslog raw:\n%s\nparsed:\n%s", raw, parsed)
	for _, want := range []string{"event via udp: ACTUAL_TEMPERATURE=21.4", "event via tcpoctet: ACTUAL_TEMPERATURE=21.4", "event via tcpnewline: ACTUAL_TEMPERATURE=21.4", "log via udp: api: change", "log via tcpoctet: api: change", "log via tcpnewline: api: change"} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw lacks %q", want)
		}
	}
	// rsyslog parsed the header and the structured data
	if !strings.Contains(parsed, `homematic|event|local0|info|ccu-lab|[homematic@32473 interface="HmIP-RF" address="000A1BE9A1B2C3:1" key="ACTUAL_TEMPERATURE" value="21.4" unit="°C" device_name="Heizung Bad" channel_name="Heizung Bad:1" room="Bad"]|`) {
		t.Errorf("parsed event: %s", parsed)
	}
	if !strings.Contains(parsed, `homematic|log|local0|info|ccu-lab|[homematic@32473 stream="audit" unit_name="occulited.service" remote="192.0.2.10" route="/api/system/v1/network" user="admin"]|`) {
		t.Errorf("parsed log: %s", parsed)
	}
}
