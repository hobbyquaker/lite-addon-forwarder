package target

import (
	"errors"
	"fmt"
	"strings"
)

// The SQL statement of an SQL target names the record's fields as :name placeholders. The
// statement is rewritten once, per dialect, into the driver's positional form ($1 … for
// PostgreSQL, ? for MySQL), and the values go to the server as bound parameters, never as text
// spliced into the SQL - so a device name cannot change the statement. The scan knows single
// quoted strings (with '' inside), double quoted identifiers, backtick identifiers, $$ bodies,
// -- and /* */ comments, and the PostgreSQL cast :: - none of those holds a placeholder.

// Statement is a rewritten statement: the SQL the driver gets and the placeholder of every
// positional parameter, in order.
type Statement struct {
	SQL    string
	Params []string
}

// RewriteStatement rewrites :name placeholders for a dialect.
func RewriteStatement(src, dialect string) (Statement, error) {
	var out strings.Builder
	st := Statement{}
	index := map[string]int{}
	s := src
	i := 0
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := skipQuoted(s, i, c)
			out.WriteString(s[i:j])
			i = j
		case c == '$' && i+1 < len(s) && s[i+1] == '$':
			j := strings.Index(s[i+2:], "$$")
			if j < 0 {
				out.WriteString(s[i:])
				i = len(s)
			} else {
				out.WriteString(s[i : i+2+j+2])
				i += 2 + j + 2
			}
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				j = len(s) - i
			}
			out.WriteString(s[i : i+j])
			i += j
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				out.WriteString(s[i:])
				i = len(s)
			} else {
				out.WriteString(s[i : i+2+j+2])
				i += 2 + j + 2
			}
		case c == ':':
			if i+1 < len(s) && s[i+1] == ':' {
				out.WriteString("::")
				i += 2
				continue
			}
			j := i + 1
			for j < len(s) && isIdent(s[j], j > i+1) {
				j++
			}
			name := s[i+1 : j]
			if name == "" || (i > 0 && isIdent(s[i-1], true)) {
				out.WriteByte(c)
				i++
				continue
			}
			if !knownPlaceholder(name) {
				return st, fmt.Errorf("unknown placeholder :%s", name)
			}
			switch dialect {
			case "postgres":
				n, ok := index[name]
				if !ok {
					st.Params = append(st.Params, name)
					n = len(st.Params)
					index[name] = n
				}
				fmt.Fprintf(&out, "$%d", n)
			default:
				st.Params = append(st.Params, name)
				out.WriteByte('?')
			}
			i = j
		default:
			out.WriteByte(c)
			i++
		}
	}
	st.SQL = out.String()
	if strings.TrimSpace(st.SQL) == "" {
		return st, errors.New("the statement is empty")
	}
	return st, nil
}

// SplitStatements splits a script into its statements at the ; outside strings, identifiers,
// $$ bodies and comments, dropping empty ones.
func SplitStatements(src string) []string {
	var out []string
	var cur strings.Builder
	s := src
	i := 0
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for i < len(s) {
		c := s[i]
		switch {
		case c == '\'' || c == '"' || c == '`':
			j := skipQuoted(s, i, c)
			cur.WriteString(s[i:j])
			i = j
		case c == '$' && i+1 < len(s) && s[i+1] == '$':
			j := strings.Index(s[i+2:], "$$")
			if j < 0 {
				cur.WriteString(s[i:])
				i = len(s)
			} else {
				cur.WriteString(s[i : i+2+j+2])
				i += 2 + j + 2
			}
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			j := strings.IndexByte(s[i:], '\n')
			if j < 0 {
				i = len(s)
			} else {
				i += j
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			j := strings.Index(s[i+2:], "*/")
			if j < 0 {
				i = len(s)
			} else {
				i += 2 + j + 2
			}
		case c == ';':
			flush()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	flush()
	return out
}

// skipQuoted answers the index after a quoted run starting at i with quote q; a doubled quote
// inside stays inside.
func skipQuoted(s string, i int, q byte) int {
	j := i + 1
	for j < len(s) {
		if s[j] == q {
			if j+1 < len(s) && s[j+1] == q {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return len(s)
}

func isIdent(c byte, digits bool) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (digits && c >= '0' && c <= '9')
}

// Placeholders lists the placeholders a statement may name: the template variables (the
// record's fields and the time in its forms); lists and dicts go in as JSON text.
var Placeholders = []string{
	"ts", "ts_rfc3339", "ts_unix", "ts_ms", "ts_ns", "kind",
	"interface", "device", "channel", "address", "key",
	"value", "value_num", "value_text", "value_bool", "type", "unit",
	"device_type", "channel_type", "device_name", "channel_name", "rooms", "functions", "source",
	"group", "name", "labels",
	"stream", "log_unit", "priority", "message", "fields",
	"record",
}

var placeholderSet = func() map[string]bool {
	m := map[string]bool{}
	for _, p := range Placeholders {
		m[p] = true
	}
	return m
}()

func knownPlaceholder(name string) bool { return placeholderSet[name] }
