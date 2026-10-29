// Package tmpl is the forwarder's template engine: Jinja-like templates (gonja v2, the Jinja2
// syntax Home Assistant users know) rendered against a record's context, sandboxed - no
// include, import or extends, no file or network access, a capped range(), an output limit and
// a time limit - with a few helpers of its own (slug, date, unix, unix_ms, iso).
//
// Why gonja v2 and not pongo2: pongo2 speaks Django's template language, where a filter's
// argument is written `join:","` and a function call does not exist; gonja v2 implements Jinja2
// proper (`join(",")`, `default("x")`, `{% if %}`, tests, macros), is tested against Python's
// Jinja2 and is maintained. Both are pure Go.
package tmpl

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nikolalohinski/gonja/v2/builtins"
	controlStructures "github.com/nikolalohinski/gonja/v2/builtins/control_structures"
	"github.com/nikolalohinski/gonja/v2/config"
	"github.com/nikolalohinski/gonja/v2/exec"
	"github.com/nikolalohinski/gonja/v2/loaders"
	"github.com/nikolalohinski/gonja/v2/parser"
)

// Limits of one render.
const (
	MaxOutput = 64 << 10
	MaxRange  = 10000
	MaxTime   = 500 * time.Millisecond
	MaxSource = 16 << 10
)

// ErrOutput is answered when a render produced more than MaxOutput bytes.
var ErrOutput = errors.New("template output too large")

// ErrTimeout is answered when a render took longer than MaxTime.
var ErrTimeout = errors.New("template render timed out")

var (
	env     *exec.Environment
	cfg     *config.Config
	loader  loaders.Loader
	envOnce sync.Once
)

// setup builds the sandboxed environment once.
func setup() {
	envOnce.Do(func() {
		cfg = config.New()
		cfg.StrictUndefined = false
		cfg.KeepTrailingNewline = true
		loader = loaders.MustNewMemoryLoader(map[string]string{})
		allowed := exec.NewControlStructureSet(map[string]parser.ControlStructureParser{})
		for _, name := range []string{"for", "if", "set", "raw", "macro", "filter", "with", "autoescape", "call"} {
			if p, ok := controlStructures.All.Get(name); ok {
				_ = allowed.Register(name, p)
			}
		}
		filters := exec.NewFilterSet(map[string]exec.FilterFunction{}).Update(builtins.Filters)
		// Jinja's int filter answers 0 for a bool; here true is 1, as a numeric store wants it
		if builtinInt, ok := filters.Get("int"); ok {
			_ = filters.Replace("int", func(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
				if in.IsBool() {
					if in.Bool() {
						return exec.AsValue(1)
					}
					return exec.AsValue(0)
				}
				return builtinInt(e, in, params)
			})
		}
		for name, fn := range ownFilters {
			if filters.Exists(name) {
				_ = filters.Replace(name, fn)
			} else {
				_ = filters.Register(name, fn)
			}
		}
		ctx := exec.EmptyContext().Update(builtins.GlobalFunctions).Update(builtins.GlobalVariables)
		ctx.Set("range", cappedRange)
		env = &exec.Environment{Context: ctx, Filters: filters, Tests: builtins.Tests, ControlStructures: allowed, Methods: builtins.Methods}
	})
}

// Template is one compiled template.
type Template struct {
	Source string
	tpl    *exec.Template
	// literal is set when the source has no template syntax at all: rendered without the engine.
	literal bool
}

// Compile parses a template. An empty source is a valid template that renders "".
func Compile(src string) (*Template, error) {
	setup()
	if len(src) > MaxSource {
		return nil, fmt.Errorf("template longer than %d bytes", MaxSource)
	}
	if !strings.Contains(src, "{{") && !strings.Contains(src, "{%") && !strings.Contains(src, "{#") {
		return &Template{Source: src, literal: true}, nil
	}
	tpl, err := exec.NewTemplate("template", cfg, loaders.MustNewShiftedLoader("template", strings.NewReader(src), loader), env)
	if err != nil {
		return nil, errors.New(cleanError(err))
	}
	t := &Template{Source: src, tpl: tpl}
	// gonja resolves filters and tests when it renders: a trial render against a sample
	// context surfaces an unknown one at compile time, so the UI can say so at once. Other
	// errors of the trial (a comparison of nil, say) are the context's, not the template's.
	if _, err := t.Render(sampleContext()); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "does not exist") || strings.Contains(msg, "not found") || strings.Contains(msg, "unknown") {
			return nil, err
		}
	}
	return t, nil
}

// sampleContext is a record-shaped context for the trial render.
func sampleContext() map[string]any {
	n := 1.0
	return map[string]any{"ts": time.Now(), "ts_rfc3339": "", "ts_unix": int64(0), "ts_ms": int64(0), "ts_ns": int64(0), "kind": "event", "interface": "", "device": "", "channel": 0, "address": "", "key": "", "value": n, "value_num": n, "value_text": "", "value_bool": false, "type": "", "unit": "", "device_type": "", "channel_type": "", "device_name": "", "channel_name": "", "rooms": []string{}, "functions": []string{}, "source": "", "group": "", "name": "", "labels": map[string]string{}, "stream": "", "log_unit": "", "priority": "", "message": "", "fields": map[string]string{}, "record": map[string]any{}}
}

// Render renders the template against a context; the output is trimmed of surrounding
// whitespace, so a template laid out over several lines renders the same as a one-liner.
func (t *Template) Render(ctx map[string]any) (string, error) {
	if t.literal {
		return strings.TrimSpace(t.Source), nil
	}
	w := &limitedWriter{}
	done := make(chan error, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- fmt.Errorf("template panicked: %v", r)
			}
		}()
		done <- t.tpl.Execute(w, exec.NewContext(ctx))
	}()
	select {
	case err := <-done:
		if err != nil {
			if errors.Is(err, ErrOutput) {
				return "", ErrOutput
			}
			return "", errors.New(cleanError(err))
		}
		return strings.TrimSpace(w.b.String()), nil
	case <-time.After(MaxTime):
		return "", ErrTimeout
	}
}

// Render compiles and renders in one step, for callers that render a source once.
func Render(src string, ctx map[string]any) (string, error) {
	t, err := Compile(src)
	if err != nil {
		return "", err
	}
	return t.Render(ctx)
}

type limitedWriter struct{ b strings.Builder }

func (w *limitedWriter) Write(p []byte) (int, error) {
	if w.b.Len()+len(p) > MaxOutput {
		return 0, ErrOutput
	}
	return w.b.Write(p)
}

// cleanError strips gonja's wrapping prefixes so the UI shows the reason.
func cleanError(err error) string {
	s := err.Error()
	for _, p := range []string{"unable to execute template: ", "failed to parse template '", "unable to parse "} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.Index(s, "': "); i > 0 && strings.HasPrefix(err.Error(), "failed to parse template") {
		s = s[i+3:]
	}
	return s
}

// cappedRange is Jinja's range() with at most MaxRange elements, so a template cannot spin.
func cappedRange(args *exec.VarArgs) *exec.Value {
	var start, stop, step int
	switch len(args.Args) {
	case 1:
		stop = args.Args[0].Integer()
		step = 1
	case 2:
		start, stop, step = args.Args[0].Integer(), args.Args[1].Integer(), 1
	case 3:
		start, stop, step = args.Args[0].Integer(), args.Args[1].Integer(), args.Args[2].Integer()
	default:
		return exec.AsValue(errors.New("range() takes 1 to 3 arguments"))
	}
	if step == 0 {
		return exec.AsValue(errors.New("range() step must not be 0"))
	}
	out := make([]int, 0)
	for i := start; (step > 0 && i < stop) || (step < 0 && i > stop); i += step {
		if len(out) >= MaxRange {
			break
		}
		out = append(out, i)
	}
	return exec.AsValue(out)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// Slug lowercases a name and replaces everything that is not a letter or a digit by one
// underscore: "Heizung Bad:1" -> "heizung_bad_1". Umlauts are transliterated first.
func Slug(s string) string {
	r := strings.NewReplacer("ä", "ae", "ö", "oe", "ü", "ue", "Ä", "ae", "Ö", "oe", "Ü", "ue", "ß", "ss")
	s = strings.ToLower(r.Replace(s))
	return strings.Trim(slugRe.ReplaceAllString(s, "_"), "_")
}

func timeOf(v *exec.Value) (time.Time, bool) {
	switch x := v.Interface().(type) {
	case time.Time:
		return x, true
	case *time.Time:
		return *x, true
	case int64:
		return time.Unix(x, 0), true
	case int:
		return time.Unix(int64(x), 0), true
	case float64:
		return time.Unix(int64(x), 0), true
	}
	return time.Time{}, false
}

var ownFilters = map[string]exec.FilterFunction{
	// slug: a name as an identifier
	"slug": func(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
		return exec.AsValue(Slug(in.String()))
	},
	// date("2006-01-02 15:04"): a time in a Go layout, in UTC unless a second argument names a zone
	"date": func(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
		t, ok := timeOf(in)
		if !ok {
			return exec.AsValue(in.String())
		}
		layout := time.RFC3339
		if len(params.Args) > 0 {
			layout = params.Args[0].String()
		}
		if len(params.Args) > 1 {
			if loc, err := time.LoadLocation(params.Args[1].String()); err == nil {
				return exec.AsValue(t.In(loc).Format(layout))
			}
		} else if params.GetKeywordArgument("local", false).Bool() {
			return exec.AsValue(t.Local().Format(layout))
		}
		return exec.AsValue(t.UTC().Format(layout))
	},
	"unix": func(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
		if t, ok := timeOf(in); ok {
			return exec.AsValue(t.Unix())
		}
		return in
	},
	"unix_ms": func(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
		if t, ok := timeOf(in); ok {
			return exec.AsValue(t.UnixMilli())
		}
		return in
	},
	"unix_ns": func(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
		if t, ok := timeOf(in); ok {
			return exec.AsValue(t.UnixNano())
		}
		return in
	},
	"iso": func(e *exec.Evaluator, in *exec.Value, params *exec.VarArgs) *exec.Value {
		if t, ok := timeOf(in); ok {
			return exec.AsValue(t.UTC().Format(time.RFC3339Nano))
		}
		return in
	},
}

// Filters documents the helpers the UI lists beside Jinja's own.
var Filters = []struct {
	Name string            `json:"name"`
	Doc  map[string]string `json:"doc"`
}{
	{"slug", map[string]string{"de": "Name als Bezeichner: klein, Sonderzeichen zu _", "en": "a name as an identifier: lower case, special characters to _"}},
	{"date", map[string]string{"de": "Zeit formatieren: ts | date(\"2006-01-02 15:04\") (Go-Layout, UTC; date(layout, \"Europe/Berlin\") für eine Zone)", "en": "format a time: ts | date(\"2006-01-02 15:04\") (Go layout, UTC; date(layout, \"Europe/Berlin\") for a zone)"}},
	{"unix", map[string]string{"de": "Zeit als Sekunden seit 1970", "en": "a time as seconds since 1970"}},
	{"unix_ms", map[string]string{"de": "Zeit als Millisekunden seit 1970", "en": "a time as milliseconds since 1970"}},
	{"unix_ns", map[string]string{"de": "Zeit als Nanosekunden seit 1970", "en": "a time as nanoseconds since 1970"}},
	{"iso", map[string]string{"de": "Zeit als RFC 3339 in UTC", "en": "a time as RFC 3339 in UTC"}},
	{"tojson", map[string]string{"de": "als JSON (record | tojson)", "en": "as JSON (record | tojson)"}},
	{"join", map[string]string{"de": "Liste verbinden: rooms | join(\",\")", "en": "join a list: rooms | join(\",\")"}},
	{"lower / upper", map[string]string{"de": "Klein-/Großschreibung", "en": "lower/upper case"}},
	{"default", map[string]string{"de": "Ersatz für leer: device_name | default(device)", "en": "a fallback for empty: device_name | default(device)"}},
	{"replace", map[string]string{"de": "ersetzen: key | replace(\"_\", \"-\")", "en": "replace: key | replace(\"_\", \"-\")"}},
	{"round", map[string]string{"de": "runden: value | round(1)", "en": "round: value | round(1)"}},
	{"int / float / string", map[string]string{"de": "Typ wandeln", "en": "convert the type"}},
	{"first / last / length", map[string]string{"de": "Listen: rooms | first", "en": "lists: rooms | first"}},
}
