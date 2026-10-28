package analytics

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/db"
	"github.com/styropyr0/visionary/sdks/go/logrules"
)

// The secret scanner reads each batch of the pattern job for credentials
// and card numbers that reached app_logs anyway (an old SDK, a shipper, a
// direct insert, a format the SDK's scrubbing misses). It redacts them in
// place and opens a Security finding (source 'logs', kind 'secret_in_logs')
// naming the service and the code that logs them, so the leak gets fixed
// where it starts. One finding per service, place and kind per day; more
// lines add to its count.

var secretDetectors = []struct {
	kind string
	re   *regexp.Regexp
	ok   func(string) bool
}{
	{"card number", regexp.MustCompile(`\b(?:\d[ -]?){12,18}\d\b`), luhnCard},
	{"GitHub token", regexp.MustCompile(`\b(?:gh[pousr]_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{22,})\b`), nil},
	{"Slack token", regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), nil},
	{"Stripe key", regexp.MustCompile(`\b[sr]k_(?:live|test)_[A-Za-z0-9]{16,}`), nil},
	{"Google API key", regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`), nil},
	{"private key", regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`), nil},
}

// luhnCard accepts 13 to 19 digits passing the Luhn check.
func luhnCard(s string) bool {
	var digits []int
	for _, c := range s {
		if c >= '0' && c <= '9' {
			digits = append(digits, int(c-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	for i := range digits {
		d := digits[len(digits)-1-i]
		if i%2 == 1 {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
	}
	return sum%10 == 0
}

// redactSecrets replaces what the detectors and the shared patterns find;
// it returns the text and the kinds found.
func redactSecrets(text string) (string, []string) {
	var kinds []string
	for _, d := range secretDetectors {
		found := false
		text = d.re.ReplaceAllStringFunc(text, func(m string) string {
			if d.ok != nil && !d.ok(m) {
				return m
			}
			found = true
			return "[REDACTED:" + strings.ReplaceAll(strings.ToLower(d.kind), " ", "-") + "]"
		})
		if found {
			kinds = append(kinds, d.kind)
		}
	}
	if scrubbed := logrules.Scrub(text); scrubbed != text {
		text = scrubbed
		kinds = append(kinds, "credential")
	}
	return text, kinds
}

// secretLeak is one finding being built: where lines leaked which kinds.
type secretLeak struct {
	service, where, logger, host, sample string
	kinds                                map[string]bool
	lines                                int64
	ids                                  []int64
}

// scanSecrets checks a batch read by the pattern job (columns id,
// created_at, service, version, message, attrs, severity, code_file,
// code_line, size, logger, host).
func (s *Server) scanSecrets(ctx context.Context, rows *db.Rows) {
	leaks := map[string]*secretLeak{}
	for _, r := range rows.Rows {
		msg, attrs := toString(r[4]), toString(r[5])
		newMsg, kinds := redactSecrets(msg)
		newAttrs := attrs
		if attrs != "" {
			var a map[string]any
			if json.Unmarshal([]byte(attrs), &a) == nil {
				changed := false
				for k, v := range a {
					str, ok := v.(string)
					if !ok {
						continue
					}
					if red, ks := redactSecrets(str); len(ks) > 0 {
						a[k], changed = red, true
						kinds = append(kinds, ks...)
					}
				}
				if changed {
					b, _ := json.Marshal(a)
					newAttrs = string(b)
				}
			}
		}
		if len(kinds) == 0 {
			continue
		}
		id := toInt(r[0])
		if err := s.db.Exec(ctx, `UPDATE app_logs SET message = ?, attrs = ? WHERE id = ?`, newMsg, nullIfEmpty(newAttrs), id); err != nil {
			s.logger.Warn("logs: redact secret", "err", err)
			continue
		}
		r[4], r[5] = newMsg, nullIfEmpty(newAttrs)
		service := toString(r[2])
		where := toString(r[7])
		if where != "" && toInt(r[8]) > 0 {
			where += ":" + strconv.FormatInt(toInt(r[8]), 10)
		}
		if where == "" {
			where = toString(r[10])
		}
		key := service + "\n" + where
		l := leaks[key]
		if l == nil {
			l = &secretLeak{service: service, where: where, logger: toString(r[10]), host: toString(r[11]), sample: clip(newMsg, 300), kinds: map[string]bool{}}
			leaks[key] = l
		}
		for _, k := range kinds {
			l.kinds[k] = true
		}
		l.lines++
		if len(l.ids) < 20 {
			l.ids = append(l.ids, id)
		}
	}
	now := time.Now().UTC()
	for _, l := range leaks {
		if err := s.recordSecretLeak(ctx, l, now); err != nil {
			s.logger.Warn("logs: secret finding", "err", err)
		}
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Server) recordSecretLeak(ctx context.Context, l *secretLeak, now time.Time) error {
	kinds := make([]string, 0, len(l.kinds))
	for k := range l.kinds {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	detail, _ := json.Marshal(map[string]any{
		"kinds": kinds, "where": l.where, "logger": l.logger, "sample": l.sample, "line_ids": l.ids,
		"note": "Redacted in Visionary. Fix the code that logs it, and rotate the credential if it is real.",
	})
	target := clip(l.where, 255)
	res, err := s.db.Query(ctx, `SELECT id FROM security_events WHERE source = 'logs' AND kind = 'secret_in_logs' AND created_at >= ? AND `+
		`COALESCE(service, '') = ? AND COALESCE(target, '') = ? ORDER BY id DESC LIMIT 1`, now.Add(-24*time.Hour).Format(tsLayout), l.service, target)
	if err != nil {
		return err
	}
	if len(res.Rows) > 0 {
		return s.db.Exec(ctx, `UPDATE security_events SET count = count + ?, detail = ? WHERE id = ?`, l.lines, string(detail), res.Rows[0][0])
	}
	s.logger.Info("security finding", "kind", "secret_in_logs", "service", l.service, "where", l.where, "count", l.lines)
	return s.db.Exec(ctx, `INSERT INTO security_events (created_at, source, kind, severity, host, service, actor, ip, target, detail, count)
VALUES (?, 'logs', 'secret_in_logs', 'warning', ?, ?, NULL, NULL, ?, ?, ?)`,
		now.Format(tsLayout), nullIfEmpty(l.host), nullIfEmpty(l.service), nullIfEmpty(target), string(detail), l.lines)
}
