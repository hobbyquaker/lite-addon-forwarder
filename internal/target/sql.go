package target

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/hobbyquaker/lite-addon-forwarder/internal/config"
	"github.com/hobbyquaker/lite-addon-forwarder/internal/record"
)

// SQL writes to PostgreSQL (TimescaleDB, CrateDB and everything else that speaks its wire
// protocol) or MySQL/MariaDB: one INSERT statement per target with :name placeholders, run for
// every record of a batch inside one transaction with bound parameters; optionally a schema
// script (CREATE TABLE IF NOT EXISTS …) run once per connection before the first batch. The
// Test runs the statements in a transaction that is rolled back.
type SQL struct {
	cfg  config.Target
	db   *sql.DB
	stmt Statement
	ddl  []string

	mu         sync.Mutex
	schemaDone bool
}

// NewSQL builds an SQL target.
func NewSQL(t config.Target) (*SQL, error) {
	if t.SQL == nil {
		return nil, errors.New("sql settings missing")
	}
	c := t.SQL
	stmt, err := RewriteStatement(c.Insert, c.Dialect)
	if err != nil {
		return nil, fmt.Errorf("INSERT: %w", err)
	}
	s := &SQL{cfg: t, stmt: stmt}
	if c.InstallSchema {
		s.ddl = SplitStatements(c.Schema)
	}
	tlsc, err := sqlTLS(t)
	if err != nil {
		return nil, err
	}
	timeout := time.Duration(t.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	switch c.Dialect {
	case "postgres":
		pc, err := pgx.ParseConfig("")
		if err != nil {
			return nil, err
		}
		pc.Host, pc.Port, pc.Database, pc.User, pc.Password = c.Host, uint16(c.Port), c.Database, c.User, t.Secrets["password"]
		pc.TLSConfig = tlsc
		pc.ConnectTimeout = 10 * time.Second
		pc.RuntimeParams["application_name"] = "lite-addon-forwarder"
		pc.DefaultQueryExecMode = pgx.QueryExecModeCacheStatement
		s.db = stdlib.OpenDB(*pc)
	case "mysql":
		mc := mysql.NewConfig()
		mc.User, mc.Passwd, mc.Net, mc.Addr, mc.DBName = c.User, t.Secrets["password"], "tcp", net.JoinHostPort(c.Host, strconv.Itoa(c.Port)), c.Database
		mc.Timeout, mc.ReadTimeout, mc.WriteTimeout = 10*time.Second, timeout, timeout
		mc.ParseTime = true
		mc.InterpolateParams = false
		mc.Params = map[string]string{"charset": "utf8mb4"}
		if tlsc != nil {
			mc.TLS = tlsc
		}
		conn, err := mysql.NewConnector(mc)
		if err != nil {
			return nil, err
		}
		s.db = sql.OpenDB(conn)
	default:
		return nil, fmt.Errorf("unknown dialect %q", c.Dialect)
	}
	s.db.SetMaxOpenConns(1)
	s.db.SetMaxIdleConns(1)
	s.db.SetConnMaxIdleTime(5 * time.Minute)
	return s, nil
}

// sqlTLS builds the TLS configuration of the connection: nil for mode disable, no check for
// require, the target's TLS settings (own CA, server name) for verify.
func sqlTLS(t config.Target) (*tls.Config, error) {
	switch t.SQL.TLSMode {
	case "", "disable":
		return nil, nil
	}
	tc := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: t.SQL.Host}
	if t.SQL.TLSMode == "require" || t.TLS.Insecure {
		tc.InsecureSkipVerify = true //nolint:gosec // the user's explicit choice, shown as such in the UI
	}
	if strings.TrimSpace(t.TLS.CA) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(t.TLS.CA)) {
			return nil, errors.New("the CA is not a PEM certificate")
		}
		tc.RootCAs = pool
	}
	if t.TLS.ServerName != "" {
		tc.ServerName = t.TLS.ServerName
	}
	return tc, nil
}

// Close closes the pool.
func (s *SQL) Close() error { return s.db.Close() }

// Encode implements Target: a payload is the batch's records as JSON (the rows are built at
// Send, from the record's context, so the parameters keep their types).
func (s *SQL) Encode(batch []*record.Record) ([]Payload, []string) {
	var payloads []Payload
	var errs []string
	var rows []*record.Record
	size := 0
	flush := func() {
		if len(rows) > 0 {
			b, _ := json.Marshal(rows)
			payloads = append(payloads, Payload{Body: b, Records: len(rows), ContentType: "application/json"})
		}
		rows = nil
		size = 0
	}
	for _, r := range batch {
		if _, err := s.args(r); err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		n := len(r.Message) + len(r.ValueText) + 256
		if size > 0 && size+n > s.cfg.Batch.MaxBytes {
			flush()
		}
		rows = append(rows, r)
		size += n
	}
	flush()
	return payloads, errs
}

// args builds the parameter values of one record in the statement's order.
func (s *SQL) args(r *record.Record) ([]any, error) {
	ctx := r.Context()
	out := make([]any, 0, len(s.stmt.Params))
	for _, p := range s.stmt.Params {
		v, ok := ctx[p]
		if !ok {
			return nil, fmt.Errorf("no value for :%s", p)
		}
		switch p {
		case "rooms", "functions", "labels", "fields", "record":
			b, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			v = string(b)
		case "value":
			switch x := v.(type) {
			case nil:
				v = nil
			case bool, string, float64, int64, int:
				v = x
			default:
				b, _ := json.Marshal(x)
				v = string(b)
			}
		case "ts":
			v = r.TS.UTC()
		}
		out = append(out, v)
	}
	return out, nil
}

func decodeRows(body []byte) ([]*record.Record, error) {
	var rows []*record.Record
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, Permanent("payload: %v", err)
	}
	for _, r := range rows {
		// JSON turned every number into a float64; an integer or enum value goes back to int64
		if f, ok := r.Value.(float64); ok && (r.Type == "INTEGER" || r.Type == "ENUM") {
			r.Value = int64(f)
		}
	}
	return rows, nil
}

// Send implements Target: the schema once, then the rows in one transaction.
func (s *SQL) Send(ctx context.Context, p Payload) error {
	rows, err := decodeRows(p.Body)
	if err != nil {
		return err
	}
	if err := s.ensureSchema(ctx); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return s.classify(err)
	}
	if err := s.insert(ctx, tx, rows); err != nil {
		_ = tx.Rollback()
		return s.classify(err)
	}
	if err := tx.Commit(); err != nil {
		return s.classify(err)
	}
	return nil
}

func (s *SQL) insert(ctx context.Context, tx *sql.Tx, rows []*record.Record) error {
	stmt, err := tx.PrepareContext(ctx, s.stmt.SQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rows {
		args, err := s.args(r)
		if err != nil {
			return &PermanentError{Err: err}
		}
		if _, err := stmt.ExecContext(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

// ensureSchema runs the schema script once per process (and again after a failure).
func (s *SQL) ensureSchema(ctx context.Context) error {
	if len(s.ddl) == 0 {
		return nil
	}
	s.mu.Lock()
	done := s.schemaDone
	s.mu.Unlock()
	if done {
		return nil
	}
	for _, d := range s.ddl {
		if _, err := s.db.ExecContext(ctx, d); err != nil {
			// a schema that cannot be applied is a configuration error: retried, so the status
			// shows it until the user fixes it, nothing dropped
			return fmt.Errorf("schema: %w", describe(err))
		}
	}
	s.mu.Lock()
	s.schemaDone = true
	s.mu.Unlock()
	return nil
}

// classify sorts a driver error: a lost connection, a busy or shutting-down server, a deadlock
// and anything the client could not deliver are retried; a statement the server refused for
// its content (a syntax error, a wrong column, a constraint, a bad value) is permanent.
func (s *SQL) classify(err error) error {
	if err == nil {
		return nil
	}
	if IsPermanent(err) {
		return err
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		class := pg.Code
		if len(class) > 2 {
			class = class[:2]
		}
		switch class {
		case "08", "53", "57", "58", "XX":
			return describe(err)
		case "40":
			return describe(err)
		}
		return Permanent("%v", describe(err))
	}
	var my *mysql.MySQLError
	if errors.As(err, &my) {
		switch my.Number {
		case 1040, 1041, 1053, 1077, 1152, 1158, 1159, 1160, 1161, 1203, 1205, 1213, 1317, 1927, 3024, 3168, 3169, 4031:
			return describe(err)
		}
		return Permanent("%v", describe(err))
	}
	// connection problems and everything unknown: retried
	return describe(err)
}

// describe gives a driver's error a readable prefix with the server's own words.
func describe(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		msg := pg.Severity + " " + pg.Code + ": " + pg.Message
		if pg.Detail != "" {
			msg += " (" + pg.Detail + ")"
		}
		return errors.New(Line(msg))
	}
	var my *mysql.MySQLError
	if errors.As(err, &my) {
		return errors.New(Line(fmt.Sprintf("error %d: %s", my.Number, my.Message)))
	}
	if errors.Is(err, driver.ErrBadConn) || errors.Is(err, io.EOF) {
		return errors.New("the connection was lost")
	}
	return errors.New(Line(err.Error()))
}

// Test implements Target: connect, the server's version, the schema and one INSERT, all inside
// a transaction that is rolled back - on MySQL a schema statement commits by itself, so there
// the schema is applied for real (it says IF NOT EXISTS) and the INSERT is rolled back.
func (s *SQL) Test(ctx context.Context, sample *record.Record) (string, error) {
	var version string
	q := "SELECT version()"
	if err := s.db.QueryRowContext(ctx, q).Scan(&version); err != nil {
		return "", s.classify(err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", s.classify(err)
	}
	defer func() { _ = tx.Rollback() }()
	var notes []string
	if len(s.ddl) > 0 {
		if s.cfg.SQL.Dialect == "mysql" {
			for _, d := range s.ddl {
				if _, err := s.db.ExecContext(ctx, d); err != nil {
					return "", fmt.Errorf("schema: %w", describe(err))
				}
			}
			notes = append(notes, fmt.Sprintf("%d schema statements applied (MySQL commits DDL by itself)", len(s.ddl)))
		} else {
			for _, d := range s.ddl {
				if _, err := tx.ExecContext(ctx, d); err != nil {
					return "", fmt.Errorf("schema: %w", describe(err))
				}
			}
			notes = append(notes, fmt.Sprintf("%d schema statements OK", len(s.ddl)))
		}
	}
	if err := s.insert(ctx, tx, []*record.Record{sample}); err != nil {
		return "", fmt.Errorf("INSERT: %w", describe(err))
	}
	notes = append(notes, "INSERT OK (1 row), rolled back")
	args, _ := s.args(sample)
	return fmt.Sprintf("%s\n%s\n%s\n%s", Line(version), strings.Join(notes, "; "), s.stmt.SQL, renderArgs(s.stmt.Params, args)), nil
}

// Preview implements Target: the rewritten statement and the parameter rows.
func (s *SQL) Preview(batch []*record.Record) (string, []string) {
	var b strings.Builder
	var errs []string
	if len(s.ddl) > 0 {
		b.WriteString("-- schema, once per connection:\n")
		for _, d := range s.ddl {
			b.WriteString(d)
			b.WriteString(";\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(s.stmt.SQL)
	b.WriteString("\n")
	for _, r := range batch {
		args, err := s.args(r)
		if err != nil {
			errs = append(errs, r.Summary()+": "+err.Error())
			continue
		}
		b.WriteString("-- ")
		b.WriteString(renderArgs(s.stmt.Params, args))
		b.WriteString("\n")
	}
	return b.String(), errs
}

func renderArgs(params []string, args []any) string {
	parts := make([]string, 0, len(params))
	for i, p := range params {
		if i >= len(args) {
			break
		}
		var v string
		switch x := args[i].(type) {
		case nil:
			v = "NULL"
		case float64:
			v = strconv.FormatFloat(x, 'g', -1, 64)
		case time.Time:
			v = x.Format(time.RFC3339Nano)
		case string:
			v = strconv.Quote(x)
		default:
			v = fmt.Sprint(x)
		}
		parts = append(parts, ":"+p+"="+v)
	}
	return strings.Join(parts, " ")
}
