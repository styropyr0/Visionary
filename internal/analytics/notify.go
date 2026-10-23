package analytics

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"
)

// Notification channels: Slack and Discord incoming webhooks, a generic JSON
// webhook (optionally signed with HMAC-SHA256) and email over SMTP. Webhook
// URLs and secrets are credentials: they are stored as given, but never
// logged, and API responses only carry masked forms.

type alertChannel struct {
	ID            int64         `json:"id"`
	Name          string        `json:"name"`
	Kind          string        `json:"kind"` // slack, discord, webhook, email
	Config        channelConfig `json:"-"`
	DigestEnabled bool          `json:"digest_enabled"`
	DigestWeekday int           `json:"digest_weekday"` // 0 = Sunday
	DigestHour    int           `json:"digest_hour"`
	DigestSentAt  string        `json:"digest_sent_at"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
}

type channelConfig struct {
	URL    string   `json:"url,omitempty"`
	Secret string   `json:"secret,omitempty"`
	To     []string `json:"to,omitempty"`
}

// channelView is what the API returns: no URL or secret in the clear.
type channelView struct {
	alertChannel
	URLMasked string   `json:"url_masked,omitempty"`
	HasSecret bool     `json:"has_secret"`
	To        []string `json:"to,omitempty"`
}

func (c alertChannel) view() channelView {
	return channelView{alertChannel: c, URLMasked: maskURL(c.Config.URL, c.Kind), HasSecret: c.Config.Secret != "", To: c.Config.To}
}

// maskURL keeps the host so people can tell channels apart, and the last
// four characters of the path; webhook paths and queries carry tokens.
func maskURL(raw, kind string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "••••"
	}
	tail := strings.TrimRight(u.Path, "/")
	if len(tail) > 4 {
		tail = tail[len(tail)-4:]
	} else {
		tail = ""
	}
	out := u.Scheme + "://" + u.Host + "/••••" + tail
	if u.RawQuery != "" {
		out += "?••••"
	}
	return out
}

var channelKinds = map[string]bool{"slack": true, "discord": true, "webhook": true, "email": true}

func validateChannelConfig(kind string, c channelConfig) error {
	switch kind {
	case "slack", "discord", "webhook":
		u, err := url.Parse(c.URL)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return errors.New("enter the webhook URL (http or https)")
		}
		if len(c.URL) > 2048 || len(c.Secret) > 512 {
			return errors.New("URL or secret is too long")
		}
	case "email":
		if len(c.To) == 0 || len(c.To) > 20 {
			return errors.New("enter 1 to 20 recipient addresses")
		}
		for _, to := range c.To {
			if a, err := mail.ParseAddress(to); err != nil || a.Address != to {
				return fmt.Errorf("%q is not an email address", to)
			}
		}
	default:
		return errors.New("kind must be slack, discord, webhook or email")
	}
	return nil
}

const channelColumns = `id, name, kind, config, digest_enabled, digest_weekday, digest_hour, digest_sent_at, created_at, updated_at`

func scanChannel(row []any) (alertChannel, bool) {
	if len(row) != 10 {
		return alertChannel{}, false
	}
	c := alertChannel{
		ID: toInt(row[0]), Name: toString(row[1]), Kind: toString(row[2]),
		DigestEnabled: toInt(row[4]) == 1, DigestWeekday: int(toInt(row[5])), DigestHour: int(toInt(row[6])),
		DigestSentAt: toString(row[7]), CreatedAt: toString(row[8]), UpdatedAt: toString(row[9]),
	}
	_ = json.Unmarshal([]byte(toString(row[3])), &c.Config)
	return c, true
}

func (s *Server) listChannels(ctx context.Context) ([]alertChannel, error) {
	res, err := s.db.Query(ctx, `SELECT `+channelColumns+` FROM alert_channels ORDER BY id`)
	if err != nil {
		return nil, err
	}
	out := make([]alertChannel, 0, len(res.Rows))
	for _, row := range res.Rows {
		if c, ok := scanChannel(row); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (s *Server) getChannel(ctx context.Context, id int64) (*alertChannel, error) {
	res, err := s.db.Query(ctx, `SELECT `+channelColumns+` FROM alert_channels WHERE id = ?`, id)
	if err != nil {
		return nil, err
	}
	for _, row := range res.Rows {
		if c, ok := scanChannel(row); ok {
			return &c, nil
		}
	}
	return nil, nil
}

func (s *Server) saveChannel(ctx context.Context, c *alertChannel) error {
	cfg, _ := json.Marshal(c.Config)
	now := time.Now().UTC().Format(tsLayout)
	c.UpdatedAt = now
	if c.ID == 0 {
		c.CreatedAt = now
		id, err := s.db.InsertID(ctx, `INSERT INTO alert_channels (name, kind, config, digest_enabled, digest_weekday, digest_hour, digest_sent_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.Name, c.Kind, string(cfg), boolInt(c.DigestEnabled), c.DigestWeekday, c.DigestHour, c.DigestSentAt, now, now)
		c.ID = id
		return err
	}
	return s.db.Exec(ctx, `UPDATE alert_channels SET name = ?, kind = ?, config = ?, digest_enabled = ?, digest_weekday = ?, digest_hour = ?, updated_at = ?
WHERE id = ?`, c.Name, c.Kind, string(cfg), boolInt(c.DigestEnabled), c.DigestWeekday, c.DigestHour, now, c.ID)
}

// notice is a message for any channel; each kind renders it its own way.
type notice struct {
	Event    string // firing, resolved, reminder, test, digest
	Title    string
	Severity string
	Lines    []string
	Fields   [][2]string
	// Note is set apart: the likely cause of a firing alert, or a digest's
	// written summary.
	Note    string
	Runbook string
	At      time.Time
	Payload map[string]any // the generic webhook's body
	// HTML is an email's formatted version, sent next to the plain text.
	HTML string
}

func (n notice) color() int {
	switch {
	case n.Event == "resolved":
		return 0x0ca30c
	case n.Event == "digest" || n.Event == "test":
		return 0x3987e5
	case n.Severity == "critical":
		return 0xd03b3b
	case n.Severity == "warning":
		return 0xfab219
	}
	return 0x3987e5
}

func (n notice) plain() string {
	var b strings.Builder
	b.WriteString(n.Title + "\n")
	for _, l := range n.Lines {
		b.WriteString(l + "\n")
	}
	for _, f := range n.Fields {
		b.WriteString(f[0] + ": " + f[1] + "\n")
	}
	if n.Note != "" {
		b.WriteString("\n" + n.Note + "\n")
	}
	if n.Runbook != "" {
		b.WriteString("\nRunbook: " + n.Runbook + "\n")
	}
	return b.String()
}

func (s *Server) alertNotice(r alertRule, ev *alertEvent, repeatSince string) notice {
	verb := map[string]string{"firing": "FIRING", "resolved": "RESOLVED"}[ev.State]
	if repeatSince != "" {
		verb = "STILL FIRING"
	}
	title := fmt.Sprintf("[%s] %s", verb, r.Name)
	if ev.SeriesKey != "" {
		title += " · " + ev.SeriesKey
	}
	n := notice{Event: ev.State, Title: title, Severity: r.Severity, Lines: []string{ev.Message}, Runbook: r.Runbook, At: time.Now().UTC()}
	if repeatSince != "" {
		n.Event = "reminder"
		if t, err := time.Parse(tsLayout, repeatSince); err == nil {
			n.Lines = append(n.Lines, "Firing for "+humanDuration(time.Since(t))+".")
		}
	}
	n.Fields = append(n.Fields, [2]string{"Severity", r.Severity})
	if ev.Value != nil {
		n.Fields = append(n.Fields, [2]string{"Value", fnum(round2(*ev.Value))})
	}
	if ev.Threshold != nil {
		n.Fields = append(n.Fields, [2]string{"Threshold", fnum(round2(*ev.Threshold))})
	}
	if ev.Explanation != "" {
		n.Note = ev.Explanation
		if !strings.HasPrefix(strings.ToLower(n.Note), "likely") {
			n.Note = "Likely cause: " + n.Note
		}
	}
	n.Payload = map[string]any{
		"event":       "alert." + n.Event,
		"rule":        map[string]any{"id": r.ID, "name": r.Name, "kind": r.Kind, "severity": r.Severity, "metric": r.Spec.Metric},
		"state":       ev.State,
		"series_key":  ev.SeriesKey,
		"value":       ev.Value,
		"threshold":   ev.Threshold,
		"message":     ev.Message,
		"explanation": ev.Explanation,
		"runbook":     r.Runbook,
		"event_id":    ev.ID,
		"at":          ev.CreatedAt,
	}
	return n
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

// sendToChannels delivers to every listed channel and reports whether at
// least one accepted it. Failures are logged without URLs or secrets.
func (s *Server) sendToChannels(ctx context.Context, ids []int64, n notice) bool {
	if len(ids) == 0 {
		return false
	}
	chans, err := s.listChannels(ctx)
	if err != nil {
		s.logger.Warn("alerts: load channels", "err", err)
		return false
	}
	want := map[int64]bool{}
	for _, id := range ids {
		want[id] = true
	}
	ok := false
	for _, c := range chans {
		if !want[c.ID] {
			continue
		}
		if err := s.sendNotice(ctx, c, n); err != nil {
			s.logger.Warn("alerts: notification failed", "channel_id", c.ID, "channel", c.Name, "kind", c.Kind, "err", err)
			continue
		}
		ok = true
	}
	return ok
}

func (s *Server) sendNotice(ctx context.Context, c alertChannel, n notice) error {
	switch c.Kind {
	case "slack":
		return postJSON(ctx, c.Config.URL, slackBody(n), nil)
	case "discord":
		return postJSON(ctx, c.Config.URL, discordBody(n), nil)
	case "webhook":
		body := map[string]any{}
		for k, v := range n.Payload {
			body[k] = v
		}
		if _, ok := body["event"]; !ok {
			body["event"] = n.Event
		}
		body["title"], body["text"] = n.Title, n.plain()
		return postJSON(ctx, c.Config.URL, body, []byte(c.Config.Secret))
	case "email":
		return s.sendEmail(ctx, c.Config.To, n.Title, n.plain(), n.HTML)
	}
	return errors.New("unknown channel kind")
}

func slackBody(n notice) map[string]any {
	text := "*" + n.Title + "*\n" + strings.Join(n.Lines, "\n")
	blocks := []any{
		map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": text}},
	}
	if len(n.Fields) > 0 {
		var fields []any
		for _, f := range head(n.Fields, 10) {
			fields = append(fields, map[string]string{"type": "mrkdwn", "text": "*" + f[0] + "*\n" + f[1]})
		}
		blocks = append(blocks, map[string]any{"type": "section", "fields": fields})
	}
	if n.Note != "" {
		blocks = append(blocks, map[string]any{"type": "section", "text": map[string]string{"type": "mrkdwn", "text": truncate(n.Note, 2900)}})
	}
	if n.Runbook != "" {
		blocks = append(blocks, map[string]any{"type": "context", "elements": []any{map[string]string{"type": "mrkdwn", "text": "Runbook: " + truncate(n.Runbook, 2900)}}})
	}
	return map[string]any{"text": n.Title, "blocks": blocks}
}

func discordBody(n notice) map[string]any {
	desc := strings.Join(n.Lines, "\n")
	if n.Note != "" {
		desc += "\n\n" + n.Note
	}
	embed := map[string]any{
		"title":       truncate(n.Title, 250),
		"description": truncate(desc, 4000),
		"color":       n.color(),
		"timestamp":   n.At.Format(time.RFC3339),
	}
	var fields []any
	for _, f := range head(n.Fields, 20) {
		fields = append(fields, map[string]any{"name": f[0], "value": truncate(f[1], 1000), "inline": true})
	}
	if n.Runbook != "" {
		fields = append(fields, map[string]any{"name": "Runbook", "value": truncate(n.Runbook, 1000)})
	}
	if fields != nil {
		embed["fields"] = fields
	}
	return map[string]any{"username": "Visionary", "embeds": []any{embed}}
}

var notifyClient = &http.Client{
	Timeout: 10 * time.Second,
	// Webhooks answer directly; following redirects would only widen where
	// the request can be sent.
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

// postJSON sends body and, with a secret, signs it: X-Visionary-Signature is
// "sha256=" + hex HMAC-SHA256 of the exact body bytes.
func postJSON(ctx context.Context, target string, body any, secret []byte) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid webhook URL")
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", UserAgent)
	if len(secret) > 0 {
		mac := hmac.New(sha256.New, secret)
		mac.Write(payload)
		req.Header.Set("X-Visionary-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}
	resp, err := notifyClient.Do(req)
	if err != nil {
		// url.Error repeats the URL, which holds the webhook token.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("post: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook answered %d", resp.StatusCode)
	}
	return nil
}
