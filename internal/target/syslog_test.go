package target

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

func syslogTarget(t *testing.T, transport string, port int, mod func(*config.Target)) *Syslog {
	t.Helper()
	cfg := config.DefaultTarget("syslog")
	cfg.Name = "sl"
	cfg.Syslog.Host, cfg.Syslog.Port, cfg.Syslog.Transport = "127.0.0.1", port, transport
	cfg.Syslog.Hostname = "ccu"
	if mod != nil {
		mod(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := NewSyslog(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSyslogMessage(t *testing.T) {
	s := syslogTarget(t, "udp", 514, nil)
	ts := time.Date(2026, 10, 30, 12, 0, 0, 500000000, time.UTC)
	r := record.Event(ts, "HmIP-RF", "ABC:1", "ACTUAL_TEMPERATURE", json.RawMessage(`21.4`), "FLOAT", nil)
	r.Unit = "°C"
	r.DeviceName, r.ChannelName, r.Rooms = "Heizung", `Heizung "Bad"`, []string{"Bad", "EG"}
	msg, err := s.Message(r)
	if err != nil {
		t.Fatal(err)
	}
	got := string(msg)
	want := `<134>1 2026-10-30T12:00:00.500000Z ccu homematic - event [homematic@32473 interface="HmIP-RF" address="ABC:1" key="ACTUAL_TEMPERATURE" value="21.4" unit="°C" device_name="Heizung" channel_name="Heizung \"Bad\"" room="Bad,EG"] Heizung "Bad" ACTUAL_TEMPERATURE = 21.4°C`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// a log line takes its priority and its fields through *
	l := record.Log(ts, "journal", "rfd.service", "warning", "duty cycle\nhigh", map[string]string{"pid": "42", "b]": "x"})
	msg, err = s.Message(l)
	if err != nil {
		t.Fatal(err)
	}
	got = string(msg)
	if !strings.HasPrefix(got, "<132>1 ") || !strings.Contains(got, ` stream="journal" unit_name="rfd.service" b_="x" pid="42"] duty cycle high`) {
		t.Errorf("log: %s", got)
	}
	// a metric is info with its labels
	m := record.Metric(ts, "cpu", "load1", 0.5, map[string]string{"host": "ccu"})
	msg, _ = s.Message(m)
	if !strings.HasPrefix(string(msg), "<134>1 ") || !strings.Contains(string(msg), `group="cpu" name="load1" host="ccu"] cpu load1 = 0.5 host=ccu`) {
		t.Errorf("metric: %s", msg)
	}
	// a severity template and no structured data
	p := syslogTarget(t, "udp", 514, func(c *config.Target) {
		c.Syslog.Severity = "{% if value_num > 20 %}warning{% else %}notice{% endif %}"
		c.Syslog.SDID = ""
		c.Syslog.Message = "{{ key }}={{ value }}"
	})
	msg, _ = p.Message(r)
	if string(msg) != "<132>1 2026-10-30T12:00:00.500000Z ccu homematic - event - ACTUAL_TEMPERATURE=21.4" {
		t.Errorf("plain: %s", msg)
	}
	bad := syslogTarget(t, "udp", 514, func(c *config.Target) { c.Syslog.Severity = "loud" })
	if _, err := bad.Message(r); err == nil || !strings.Contains(err.Error(), "not a syslog severity") {
		t.Errorf("bad severity: %v", err)
	}
}

func TestSyslogUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	port := pc.LocalAddr().(*net.UDPAddr).Port
	s := syslogTarget(t, "udp", port, nil)
	ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	payloads, errs := s.Encode([]*record.Record{record.Metric(ts, "cpu", "a", 1, nil), record.Metric(ts, "cpu", "b", 2, nil)})
	if len(errs) != 0 || len(payloads) != 1 || payloads[0].Records != 2 {
		t.Fatalf("encode %v %d", errs, len(payloads))
	}
	msgs, err := unframe(payloads[0].Body)
	if err != nil || len(msgs) != 2 {
		t.Fatalf("unframe %v %d", err, len(msgs))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Send(ctx, payloads[0]); err != nil {
		t.Fatal(err)
	}
	_ = pc.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 65536)
	for i := 0; i < 2; i++ {
		n, _, err := pc.ReadFrom(buf)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(buf[:n]), "<134>1 2026-10-30T12:00:00.000000Z ccu homematic - metric [homematic@32473 ") {
			t.Errorf("datagram %d: %s", i, buf[:n])
		}
	}
	if msg, err := s.Test(ctx, Sample()); err != nil || !strings.Contains(msg, "sent over UDP") {
		t.Errorf("test: %q %v", msg, err)
	}
}

func TestSyslogTCPFraming(t *testing.T) {
	for _, framing := range []string{"octet", "newline"} {
		t.Run(framing, func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			got := make(chan string, 1)
			go func() {
				c, err := l.Accept()
				if err != nil {
					return
				}
				defer c.Close()
				_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReader(c)
				var all strings.Builder
				for i := 0; i < 2; i++ {
					if framing == "newline" {
						line, err := r.ReadString('\n')
						if err != nil {
							break
						}
						all.WriteString(line)
						continue
					}
					lenStr, err := r.ReadString(' ')
					if err != nil {
						break
					}
					n, _ := strconv.Atoi(strings.TrimSpace(lenStr))
					buf := make([]byte, n)
					if _, err := readFull(r, buf); err != nil {
						break
					}
					all.WriteString(lenStr)
					all.Write(buf)
				}
				got <- all.String()
			}()
			s := syslogTarget(t, "tcp", l.Addr().(*net.TCPAddr).Port, func(c *config.Target) { c.Syslog.Framing = framing })
			ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
			payloads, _ := s.Encode([]*record.Record{record.Log(ts, "journal", "a.service", "info", "one", nil), record.Log(ts, "journal", "a.service", "err", "two", nil)})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.Send(ctx, payloads[0]); err != nil {
				t.Fatal(err)
			}
			select {
			case all := <-got:
				if framing == "newline" {
					if !strings.HasSuffix(all, " one\n<131>1 2026-10-30T12:00:00.000000Z ccu homematic - log [homematic@32473 stream=\"journal\" unit_name=\"a.service\"] two\n") {
						t.Errorf("newline: %q", all)
					}
				} else if !strings.Contains(all, " one") || !strings.Contains(all, " two") || !strings.HasPrefix(all, strconv.Itoa(len(all)/2-3)) && !strings.Contains(all, " <134>1 ") {
					t.Errorf("octet: %q", all)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("nothing received")
			}
		})
	}
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := r.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func TestSyslogRefused(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	s := syslogTarget(t, "tcp", port, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	payloads, _ := s.Encode([]*record.Record{Sample()})
	if err := s.Send(ctx, payloads[0]); err == nil || IsPermanent(err) {
		t.Errorf("refused: %v", err)
	}
}
