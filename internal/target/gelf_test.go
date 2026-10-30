package target

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

func gelfTarget(t *testing.T, transport string, port int, mod func(*config.Target)) *GELF {
	t.Helper()
	cfg := config.DefaultTarget("gelf")
	cfg.Name = "gl"
	cfg.GELF.Host, cfg.GELF.Port, cfg.GELF.Transport = "127.0.0.1", port, transport
	if mod != nil {
		mod(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	g, err := NewGELF(cfg)
	if err != nil {
		t.Fatal(err)
	}
	g.host = "ccu"
	t.Cleanup(func() { _ = g.Close() })
	return g
}

func TestGELFMessage(t *testing.T) {
	g := gelfTarget(t, "udp", 12201, nil)
	ts := time.Date(2026, 10, 30, 12, 0, 0, 250000000, time.UTC)
	r := record.Event(ts, "HmIP-RF", "ABC:1", "LEVEL", json.RawMessage(`0.5`), "FLOAT", nil)
	r.ChannelName, r.Rooms = "Rollo", []string{"Bad"}
	b, err := g.Message(r)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["version"] != "1.1" || doc["host"] != "ccu" || doc["short_message"] != "Rollo LEVEL = 0.5" || doc["level"] != 6.0 || doc["timestamp"].(float64)-float64(ts.UnixNano())/1e9 > 1e-6 {
		t.Errorf("doc %v", doc)
	}
	if doc["_kind"] != "event" || doc["_address"] != "ABC:1" || doc["_value"] != 0.5 || doc["_rooms"] != "Bad" || doc["_ts"] != "2026-10-30T12:00:00.25Z" || doc["_channel"] != 1.0 {
		t.Errorf("fields %v", doc)
	}
	if _, ok := doc["_id"]; ok {
		t.Error("_id")
	}
	// a log line: its priority as the level, its fields through *
	l := record.Log(ts, "audit", "occulited.service", "warning", "api: change", map[string]string{"user": "admin", "bad name!": "x"})
	b, _ = g.Message(l)
	_ = json.Unmarshal(b, &doc)
	if doc["level"] != 4.0 || doc["_user"] != "admin" || doc["_bad_name_"] != "x" || doc["_stream"] != "audit" || doc["short_message"] != "api: change" {
		t.Errorf("log %v", doc)
	}
	// the compact preset and a level template
	c := gelfTarget(t, "udp", 12201, func(c *config.Target) {
		p := config.GELFPresets["compact"]
		c.GELF.Fields, c.GELF.AllFields, c.GELF.Level, c.GELF.FullMessage = p.Fields, false, "{{ 2 }}", "{{ record | tojson }}"
	})
	b, _ = c.Message(r)
	_ = json.Unmarshal(b, &doc)
	if doc["level"] != 2.0 || doc["_room"] != "Bad" || doc["_key"] != "LEVEL" || doc["_device_type"] != nil || !strings.Contains(doc["full_message"].(string), `"key":"LEVEL"`) {
		t.Errorf("compact %v", doc)
	}
}

func TestGELFUDPGzipAndChunks(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	g := gelfTarget(t, "udp", port, func(c *config.Target) { c.GELF.Compression = "gzip" })
	ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	small := record.Log(ts, "journal", "a.service", "info", "hello", nil)
	payloads, errs := g.Encode([]*record.Record{small})
	if len(errs) != 0 || len(payloads) != 1 || payloads[0].Records != 1 {
		t.Fatalf("encode %v %d", errs, len(payloads))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 65536)
	// one gzip document in one datagram
	n, _, err := pc.ReadFrom(buf)
	if err != nil {
		t.Fatal(err)
	}
	doc := gunzip(t, buf[:n])
	if !strings.Contains(doc, `"short_message":"hello"`) {
		t.Errorf("small: %s", doc)
	}
	// a big uncompressed one arrives in chunks: reassemble them
	plain := gelfTarget(t, "udp", port, func(c *config.Target) { c.GELF.Compression = "none" })
	big := record.Log(ts, "journal", "a.service", "info", strings.Repeat("x", 20000), nil)
	payloads, _ = plain.Encode([]*record.Record{big})
	if err := plain.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}
	var chunks [][]byte
	total := 0
	for {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		if buf[0] != 0x1e || buf[1] != 0x0f {
			t.Fatalf("not a chunk: % x", buf[:12])
		}
		seq, count := int(buf[10]), int(buf[11])
		if total == 0 {
			total = count
			chunks = make([][]byte, count)
		}
		chunks[seq] = append([]byte(nil), buf[12:n]...)
		if seq == count-1 {
			break
		}
	}
	if total < 2 {
		t.Fatalf("chunks: %d", total)
	}
	doc = string(bytes.Join(chunks, nil))
	if !json.Valid([]byte(doc)) || !strings.Contains(doc, strings.Repeat("x", 20000)) {
		t.Errorf("big: %d bytes", len(doc))
	}
	// too big for 128 chunks
	none := gelfTarget(t, "udp", 1, func(c *config.Target) { c.GELF.Compression = "none" })
	if _, err := none.datagrams([]byte(strings.Repeat("y", 200000))); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("huge: %v", err)
	}
	if msg, err := g.Test(ctx, Sample()); err != nil || !strings.Contains(msg, "sent over UDP (gzip, 1 datagram(s)") {
		t.Errorf("test: %q %v", msg, err)
	}
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	out, _ := io.ReadAll(zr)
	return string(out)
}

func TestGELFTCP(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		var all []byte
		buf := make([]byte, 4096)
		for bytes.Count(all, []byte{0}) < 2 {
			n, err := c.Read(buf)
			all = append(all, buf[:n]...)
			if err != nil {
				break
			}
		}
		got <- all
	}()
	g := gelfTarget(t, "tcp", l.Addr().(*net.TCPAddr).Port, nil)
	ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	payloads, _ := g.Encode([]*record.Record{record.Metric(ts, "cpu", "a", 1, nil), record.Metric(ts, "cpu", "b", 2, nil)})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := g.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}
	select {
	case all := <-got:
		docs := bytes.Split(bytes.TrimSuffix(all, []byte{0}), []byte{0})
		if len(docs) != 2 || !json.Valid(docs[0]) || !json.Valid(docs[1]) || !strings.Contains(string(docs[1]), `"_name":"b"`) {
			t.Errorf("tcp: %q", all)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing received")
	}
}
