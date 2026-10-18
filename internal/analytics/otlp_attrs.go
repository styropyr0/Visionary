package analytics

import (
	"crypto/sha1"
	"encoding/hex"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
)

// otlpAttrs is an attribute list by key.
type otlpAttrs map[string]*commonpb.AnyValue

func otlpAttrMap(kvs []*commonpb.KeyValue) otlpAttrs {
	m := make(otlpAttrs, len(kvs))
	for _, kv := range kvs {
		if kv != nil && kv.Value != nil {
			m[kv.Key] = kv.Value
		}
	}
	return m
}

// str is the first of keys that has a non-empty value, as text.
func (a otlpAttrs) str(keys ...string) string {
	for _, k := range keys {
		v := a[k]
		if v == nil {
			continue
		}
		var s string
		switch t := v.Value.(type) {
		case *commonpb.AnyValue_StringValue:
			s = t.StringValue
		case *commonpb.AnyValue_IntValue:
			s = strconv.FormatInt(t.IntValue, 10)
		case *commonpb.AnyValue_DoubleValue:
			s = strconv.FormatFloat(t.DoubleValue, 'f', -1, 64)
		case *commonpb.AnyValue_BoolValue:
			s = strconv.FormatBool(t.BoolValue)
		case *commonpb.AnyValue_ArrayValue:
			// Header attributes are string arrays.
			var parts []string
			for _, x := range t.ArrayValue.GetValues() {
				if sv := x.GetStringValue(); sv != "" {
					parts = append(parts, sv)
				}
			}
			s = strings.Join(parts, ", ")
		}
		if s = strings.TrimSpace(s); s != "" {
			return s
		}
	}
	return ""
}

// num is the first of keys with a numeric value (numeric strings count).
func (a otlpAttrs) num(keys ...string) (float64, bool) {
	for _, k := range keys {
		switch t := a[k].GetValue().(type) {
		case *commonpb.AnyValue_IntValue:
			return float64(t.IntValue), true
		case *commonpb.AnyValue_DoubleValue:
			return t.DoubleValue, true
		case *commonpb.AnyValue_StringValue:
			if f, err := strconv.ParseFloat(strings.TrimSpace(t.StringValue), 64); err == nil {
				return f, true
			}
		}
	}
	return 0, false
}

func (a otlpAttrs) boolean(key string) bool {
	switch t := a[key].GetValue().(type) {
	case *commonpb.AnyValue_BoolValue:
		return t.BoolValue
	case *commonpb.AnyValue_StringValue:
		b, _ := strconv.ParseBool(t.StringValue)
		return b
	}
	return false
}

// otlpResource is what Visionary reads from a resource.
type otlpResource struct {
	service, version, instance, runtime, sdkVersion string
	language                                       string // telemetry.sdk.language
}

func otlpResourceOf(attrs []*commonpb.KeyValue) *otlpResource {
	a := otlpAttrMap(attrs)
	r := &otlpResource{
		service:  otlpClip(a.str("service.name"), 255),
		version:  otlpClip(a.str("service.version"), 255),
		instance: otlpClip(a.str("service.instance.id", "host.name", "host.id"), 255),
	}
	if r.instance == "" {
		r.instance = r.service
	}
	if r.instance == "" {
		r.instance = "otlp"
	}
	if name := a.str("process.runtime.name"); name != "" {
		r.runtime = otlpClip(strings.TrimSpace(name+" "+a.str("process.runtime.version")), 255)
	}
	r.language = otlpClip(a.str("telemetry.sdk.language"), 64)
	if v := a.str("telemetry.sdk.version"); v != "" {
		r.sdkVersion = otlpClip(strings.TrimSpace(a.str("telemetry.sdk.language")+" "+v), 255)
	}
	return r
}

// runtimeOrLanguage is the runtime, or the SDK language when the exporter sent
// no runtime attributes, so the dashboard can still tell the language apart.
func (r *otlpResource) runtimeOrLanguage() string {
	if r.runtime != "" {
		return r.runtime
	}
	return r.language
}

func otlpHexID(b []byte) string {
	for _, x := range b {
		if x != 0 {
			return hex.EncodeToString(b)
		}
	}
	return ""
}

func otlpTime(ns uint64) string { return time.Unix(0, int64(ns)).UTC().Format(tsLayout) }

func otlpRound(ms float64) float64 { return math.Round(ms*1000) / 1000 }

// clip cuts s to at most n bytes without splitting a UTF-8 sequence.
func otlpClip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// otlpNullStr is NULL for an empty string.
func otlpNullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func otlpNullNum(v float64, ok bool) any {
	if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return v
}

var (
	otlpSQLStringLit = regexp.MustCompile(`'(?:[^']|'')*'`)
	otlpSQLNumberLit = regexp.MustCompile(`[$:@]?\b\d+(?:\.\d+)?\b`)
	otlpSQLInList    = regexp.MustCompile(`(?i)\bIN\s*\(\s*\?(?:\s*,\s*\?)*\s*\)`)
	otlpSQLSpace     = regexp.MustCompile(`\s+`)
	otlpSQLMainTable = regexp.MustCompile("(?i)^(SELECT\\b.*?\\bFROM|INSERT\\s+(?:OR\\s+\\w+\\s+)?INTO|REPLACE\\s+INTO|UPDATE|DELETE\\s+FROM)\\s+([\\w.\"`\\[\\]]+)")
	otlpSQLFirstWord = regexp.MustCompile(`^\(?\s*([A-Za-z]+)`)
	otlpDigitRun     = regexp.MustCompile(`\d+`)
	otlpTableName    = strings.NewReplacer(`"`, "", "`", "", "[", "", "]", "")
)

// otlpSanitizeSQL replaces literals with ? as the SDK contract describes.
func otlpSanitizeSQL(q string) string {
	q = otlpSQLStringLit.ReplaceAllString(q, "?")
	q = otlpSQLNumberLit.ReplaceAllStringFunc(q, func(m string) string {
		if m[0] == '$' || m[0] == ':' || m[0] == '@' {
			return m // a bind parameter, not a literal
		}
		return "?"
	})
	q = otlpSQLInList.ReplaceAllString(q, "IN (?)")
	return strings.TrimSpace(otlpSQLSpace.ReplaceAllString(q, " "))
}

// otlpSQLName is the first keyword and main table of a statement, e.g.
// "SELECT users".
func otlpSQLName(q string) string {
	q = strings.TrimSpace(q)
	if m := otlpSQLMainTable.FindStringSubmatch(q); m != nil {
		return strings.ToUpper(otlpSQLFirstWord.FindStringSubmatch(q)[1]) + " " + otlpTableName.Replace(m[2])
	}
	if m := otlpSQLFirstWord.FindStringSubmatch(q); m != nil {
		return strings.ToUpper(m[1])
	}
	return ""
}

// Stack frame formats of the common OpenTelemetry SDK languages.
var (
	otlpFramePython = regexp.MustCompile(`^\s*File "([^"]+)", line \d+, in (\S+)`)
	otlpFrameJava   = regexp.MustCompile(`^\s*at ([\w$.<>/]+)\(([^:)]*)`)
	otlpFrameDotnet = regexp.MustCompile(`^\s*at ([^(]+)\([^)]*\)(?: in (.+?):line \d+)?`)
	otlpFrameNode   = regexp.MustCompile(`^\s*at (?:(.+?) \()?(.+?):\d+:\d+\)?\s*$`)
	otlpFrameRuby   = regexp.MustCompile("^\\s*(?:from )?([^:]+\\.rb):\\d+:in [`'](.+)'")
	otlpFrameGoFile = regexp.MustCompile(`^\t(.+\.go):\d+`)
)

// otlpStackFrames reads function@file frames from a stack trace, innermost
// first, keeping only the application's own when it can tell them apart.
func otlpStackFrames(stack string) []string {
	type frame struct {
		fn, file string
		lib      bool
	}
	var frames []frame
	lines := strings.Split(strings.ReplaceAll(stack, "\r\n", "\n"), "\n")
	python := false
	for i, line := range lines {
		if m := otlpFramePython.FindStringSubmatch(line); m != nil {
			python = true
			f := strings.ToLower(strings.ReplaceAll(m[1], `\`, "/"))
			lib := strings.Contains(f, "-packages/") || strings.Contains(f, "/lib/python") || strings.HasPrefix(f, "<")
			frames = append(frames, frame{m[2], m[1], lib})
		} else if m := otlpFrameGoFile.FindStringSubmatch(line); m != nil && i > 0 {
			fn := strings.TrimSpace(lines[i-1])
			if j := strings.LastIndex(fn, "("); j > 0 {
				fn = fn[:j]
			}
			first := strings.SplitN(fn, "/", 2)[0]
			std := !strings.Contains(first, ".") && !strings.HasPrefix(fn, "main.")
			frames = append(frames, frame{fn, m[1], std || strings.Contains(fn, "go.opentelemetry.io/")})
		} else if m := otlpFrameJava.FindStringSubmatch(line); m != nil && !strings.Contains(line, " in ") {
			fn := m[1]
			lib := false
			for _, p := range []string{"java.", "javax.", "jdk.", "sun.", "kotlin.", "scala.", "org.springframework.", "org.apache.", "io.opentelemetry."} {
				lib = lib || strings.HasPrefix(fn, p)
			}
			frames = append(frames, frame{fn, m[2], lib})
		} else if m := otlpFrameDotnet.FindStringSubmatch(line); m != nil && strings.Contains(line, " in ") {
			fn := strings.TrimSpace(m[1])
			frames = append(frames, frame{fn, m[2], strings.HasPrefix(fn, "System.") || strings.HasPrefix(fn, "Microsoft.")})
		} else if m := otlpFrameNode.FindStringSubmatch(line); m != nil {
			frames = append(frames, frame{m[1], m[2], strings.Contains(m[2], "node_modules") || strings.HasPrefix(m[2], "node:")})
		} else if m := otlpFrameRuby.FindStringSubmatch(line); m != nil {
			frames = append(frames, frame{m[2], m[1], strings.Contains(m[1], "/gems/")})
		}
	}
	if python {
		// Python prints the innermost frame last.
		for i, j := 0, len(frames)-1; i < j; i, j = i+1, j-1 {
			frames[i], frames[j] = frames[j], frames[i]
		}
	}
	var app, all []string
	for _, f := range frames {
		s := f.fn + "@" + f.file
		all = append(all, s)
		if !f.lib {
			app = append(app, s)
		}
	}
	if len(app) == 0 {
		app = all
	}
	if len(app) > 5 {
		app = app[:5]
	}
	return app
}

// otlpFingerprint groups errors as the SDK contract describes.
func otlpFingerprint(typ, message, stack string) string {
	tail := strings.Join(otlpStackFrames(stack), "\n")
	if tail == "" {
		tail = otlpDigitRun.ReplaceAllString(message, "0")
	}
	sum := sha1.Sum([]byte(typ + "\n" + tail))
	return hex.EncodeToString(sum[:])[:16]
}
