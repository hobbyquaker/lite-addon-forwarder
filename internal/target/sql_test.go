package target

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

func TestRewriteStatement(t *testing.T) {
	cases := []struct {
		name, src, dialect, want string
		params                   []string
		err                      string
	}{
		{"postgres numbers and dedupes", "INSERT INTO t (a, b, c) VALUES (:ts, :key, :ts)", "postgres", "INSERT INTO t (a, b, c) VALUES ($1, $2, $1)", []string{"ts", "key"}, ""},
		{"mysql question marks", "INSERT INTO t (a, b, c) VALUES (:ts, :key, :ts)", "mysql", "INSERT INTO t (a, b, c) VALUES (?, ?, ?)", []string{"ts", "key", "ts"}, ""},
		{"a cast is not a placeholder", "VALUES (:rooms::jsonb, :value_num)", "postgres", "VALUES ($1::jsonb, $2)", []string{"rooms", "value_num"}, ""},
		{"strings, identifiers and comments stay", "-- :key here\nINSERT INTO \"t:x\" (k) VALUES (':nope', :key) /* :value */", "postgres", "-- :key here\nINSERT INTO \"t:x\" (k) VALUES (':nope', $1) /* :value */", []string{"key"}, ""},
		{"a doubled quote stays inside the string", `VALUES ('it''s :x', :key)`, "mysql", `VALUES ('it''s :x', ?)`, []string{"key"}, ""},
		{"a dollar body stays", `DO $$ BEGIN :key END $$; SELECT :key`, "postgres", `DO $$ BEGIN :key END $$; SELECT $1`, []string{"key"}, ""},
		{"an unknown placeholder is refused", "VALUES (:nope)", "postgres", "", nil, "unknown placeholder :nope"},
		{"a time literal is not a placeholder", "VALUES ('12:30', :ts)", "postgres", "VALUES ('12:30', $1)", []string{"ts"}, ""},
		{"empty", "  ", "postgres", "", nil, "empty"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st, err := RewriteStatement(c.src, c.dialect)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err %v want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if st.SQL != c.want {
				t.Errorf("sql %q want %q", st.SQL, c.want)
			}
			if strings.Join(st.Params, ",") != strings.Join(c.params, ",") {
				t.Errorf("params %v want %v", st.Params, c.params)
			}
		})
	}
}

func TestSplitStatements(t *testing.T) {
	got := SplitStatements("CREATE TABLE t (a text); -- a comment; with a semicolon\nINSERT INTO t VALUES ('a;b');\n\n/* x; */ SELECT 1;")
	want := []string{"CREATE TABLE t (a text)", "INSERT INTO t VALUES ('a;b')", "SELECT 1"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
	if len(SplitStatements("  ;; ")) != 0 {
		t.Error("empty")
	}
}

func sqlTarget(t *testing.T, dialect, insert string) *SQL {
	t.Helper()
	cfg := config.DefaultTarget("sql")
	cfg.Name = "db"
	cfg.SQL.Dialect = dialect
	cfg.SQL.Host, cfg.SQL.Database, cfg.SQL.User = "127.0.0.1", "hm", "u"
	cfg.SQL.Insert = insert
	cfg.SQL.Schema = "CREATE TABLE IF NOT EXISTS t (a text);\nCREATE INDEX IF NOT EXISTS i ON t (a)"
	cfg.Secrets = map[string]string{"password": "pw"}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	s, err := NewSQL(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSQLArgsEncodePreview(t *testing.T) {
	s := sqlTarget(t, "postgres", "INSERT INTO t VALUES (:ts, :channel, :value, :value_num, :value_bool, :rooms, :labels, :record, :message)")
	ts := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	r := record.Event(ts, "HmIP-RF", "ABC:1", "LEVEL", json.RawMessage(`1`), "INTEGER", nil)
	r.Rooms = []string{"Bad"}
	args, err := s.args(r)
	if err != nil {
		t.Fatal(err)
	}
	if args[0] != ts || args[1] != 1 || args[2] != int64(1) || args[3] != 1.0 || args[4] != false || args[5] != `["Bad"]` || args[6] != "{}" || args[8] != "" {
		t.Errorf("args %#v", args)
	}
	if !strings.Contains(args[7].(string), `"key":"LEVEL"`) {
		t.Errorf("record %v", args[7])
	}
	// a bool value and a nil number
	b := record.Event(ts, "HmIP-RF", "ABC:1", "STATE", json.RawMessage(`true`), "BOOL", nil)
	args, _ = s.args(b)
	if args[2] != true || args[4] != true {
		t.Errorf("bool %#v", args)
	}
	str := record.Event(ts, "HmIP-RF", "ABC:1", "NAME", json.RawMessage(`"x"`), "STRING", nil)
	args, _ = s.args(str)
	if args[3] != nil {
		t.Errorf("nil num %#v", args[3])
	}
	// encode and decode keep the integer
	payloads, errs := s.Encode([]*record.Record{r, b})
	if len(errs) != 0 || len(payloads) != 1 || payloads[0].Records != 2 {
		t.Fatalf("encode %v %v", errs, payloads)
	}
	rows, err := decodeRows(payloads[0].Body)
	if err != nil || len(rows) != 2 || rows[0].Value != int64(1) || rows[1].Value != true {
		t.Fatalf("decode %v %#v", err, rows)
	}
	text, errs := s.Preview([]*record.Record{r})
	if len(errs) != 0 || !strings.Contains(text, "-- schema, once per connection:") || !strings.Contains(text, "$1, $2, $3") || !strings.Contains(text, ":rooms=\"[\\\"Bad\\\"]\"") {
		t.Errorf("preview %v\n%s", errs, text)
	}
	// the mysql form
	m := sqlTarget(t, "mysql", "INSERT INTO t VALUES (:ts, :ts)")
	if m.stmt.SQL != "INSERT INTO t VALUES (?, ?)" || len(m.stmt.Params) != 2 {
		t.Errorf("mysql %+v", m.stmt)
	}
}

func TestSQLClassify(t *testing.T) {
	s := sqlTarget(t, "postgres", "INSERT INTO t VALUES (:ts)")
	cases := []struct {
		err       error
		permanent bool
		text      string
	}{
		{&pgconn.PgError{Severity: "ERROR", Code: "42P01", Message: `relation "t" does not exist`}, true, "42P01"},
		{&pgconn.PgError{Severity: "ERROR", Code: "23505", Message: "duplicate key"}, true, "duplicate"},
		{&pgconn.PgError{Severity: "FATAL", Code: "08006", Message: "connection failure"}, false, "08006"},
		{&pgconn.PgError{Severity: "ERROR", Code: "53300", Message: "too many connections"}, false, "53300"},
		{&pgconn.PgError{Severity: "FATAL", Code: "28P01", Message: "password authentication failed"}, true, "28P01"},
		{&mysql.MySQLError{Number: 1146, Message: "Table 'hm.t' doesn't exist"}, true, "1146"},
		{&mysql.MySQLError{Number: 1040, Message: "Too many connections"}, false, "1040"},
		{&mysql.MySQLError{Number: 1213, Message: "Deadlock"}, false, "1213"},
		{errors.New("dial tcp: connection refused"), false, "refused"},
	}
	for _, c := range cases {
		got := s.classify(c.err)
		if IsPermanent(got) != c.permanent || !strings.Contains(got.Error(), c.text) {
			t.Errorf("%v: permanent=%v %q", c.err, IsPermanent(got), got)
		}
	}
	if s.classify(nil) != nil {
		t.Error("nil")
	}
}

func TestSQLValidateDefaults(t *testing.T) {
	cfg := config.DefaultTarget("sql")
	cfg.Name = "db"
	cfg.SQL.Host, cfg.SQL.Database = "db.example", "hm"
	cfg.SQL.Dialect, cfg.SQL.Port = "mysql", 0
	if err := cfg.Validate(); err != nil || cfg.SQL.Port != 3306 || cfg.SQL.TLSMode != "disable" {
		t.Errorf("%v %+v", err, cfg.SQL)
	}
	cfg.SQL.Dialect = "sqlite"
	if err := cfg.Validate(); err == nil {
		t.Error("dialect")
	}
	cfg.SQL.Dialect = "postgres"
	cfg.SQL.Insert = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "INSERT") {
		t.Errorf("insert: %v", err)
	}
	cfg.SQL.Insert = "INSERT INTO t VALUES (:nope)"
	if _, err := NewSQL(cfg); err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
		t.Errorf("placeholder: %v", err)
	}
	// every preset's statements rewrite for its dialect
	for name, p := range config.SQLPresets {
		if _, err := RewriteStatement(p.Postgres.Insert, "postgres"); err != nil {
			t.Errorf("%s postgres: %v", name, err)
		}
		if n := len(SplitStatements(p.Postgres.Schema)); n < 1 {
			t.Errorf("%s postgres schema: %d", name, n)
		}
		if !p.TimescaleOnly {
			if _, err := RewriteStatement(p.MySQL.Insert, "mysql"); err != nil {
				t.Errorf("%s mysql: %v", name, err)
			}
		}
	}
}
