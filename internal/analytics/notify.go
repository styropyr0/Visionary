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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Notification channels: Slack, Discord, Microsoft Teams and Google Chat
// incoming webhooks, a generic JSON webhook (optionally signed with
// HMAC-SHA256), email over SMTP, PagerDuty and Opsgenie (which open, update
// and close incidents on their side), Telegram and SMS through Twilio
// (notify_more.go). Webhook URLs, keys and tokens are credentials: they are
// stored as given, but never logged, and API responses only carry masked
// forms.

type alertChannel struct {
	ID            int64         `json:"id"`
	Name          string        `json:"name"`
	Kind          string        `json:"kind"` // see channelKinds
	Config        channelConfig `json:"-"`
	DigestEnabled bool          `json:"digest_enabled"`
	DigestWeekday int           `json:"digest_weekday"` // 0 = Sunday
	DigestHour    int           `json:"digest_hour"`
	DigestSentAt  string        `json:"digest_sent_at"`
	CreatedAt     string        `json:"created_at"`
	UpdatedAt     string        `json:"updated_at"`
}

type channelConfig struct {
	URL string `json:"url,omitempty"`
	// Secret is the webhook signing secret, the PagerDuty routing key, the
	// Opsgenie API key, the Telegram bot token or the Twilio auth token.
	Secret string `json:"secret,omitempty"`
	// To is email addresses, or phone numbers for SMS.
	To         []string `json:"to,omitempty"`
	ChatID     string   `json:"chat_id,omitempty"`     // Telegram
	AccountSID string   `json:"account_sid,omitempty"` // Twilio
	From       string   `json:"from,omitempty"`        // Twilio sender number
	Region     string   `json:"region,omitempty"`      // Opsgenie: us or eu
}

// channelView is what the API returns: no URL or secret in the clear.
type channelView struct {
	alertChannel
	URLMasked  string   `json:"url_masked,omitempty"`
	HasSecret  bool     `json:"has_secret"`
	SecretHint string   `json:"secret_hint,omitempty"`
	To         []string `json:"to,omitempty"`
	ChatID     string   `json:"chat_id,omitempty"`
	AccountSID string   `json:"account_sid,omitempty"`
	From       string   `json:"from,omitempty"`
	Region     string   `json:"region,omitempty"`
}

func (c alertChannel) view() channelView {
	v := channelView{alertChannel: c, URLMasked: maskURL(c.Config.URL), HasSecret: c.Config.Secret != "", To: c.Config.To,
		ChatID: c.Config.ChatID, AccountSID: c.Config.AccountSID, From: c.Config.From, Region: c.Config.Region}
	// Keys are told apart by their last four characters, never shown whole.
	if sec := c.Config.Secret; len(sec) >= 12 && c.Kind != "webhook" {
		v.SecretHint = "••••" + sec[len(sec)-4:]
	}
	return v
}

// maskURL keeps the host so people can tell channels apart, and the last
// four characters of the path; webhook paths and queries carry tokens.
func maskURL(raw string) string {
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

var channelKinds = map[string]bool{
	"slack": true, "discord": true, "teams": true, "google_chat": true, "webhook": true, "email": true,
	"pagerduty": true, "opsgenie": true, "telegram": true, "sms": true,
}

// Which parts of channelConfig each kind uses.
var (
	urlKinds    = map[string]bool{"slack": true, "discord": true, "teams": true, "google_chat": true, "webhook": true}
	secretKinds = map[string]bool{"webhook": true, "pagerduty": true, "opsgenie": true, "telegram": true, "sms": true}
	// Paging services and SMS are for alerts; a weekly report does not belong there.
	noDigestKinds = map[string]bool{"pagerduty": true, "opsgenie": true, "sms": true}
)

func validateChannelConfig(kind string, c channelConfig) error {
	if len(c.Secret) > 512 || len(c.ChatID) > 64 || len(c.AccountSID) > 64 || len(c.From) > 32 {
		return errors.New("a setting is too long")
	}
	switch kind {
	case "slack", "discord", "teams", "google_chat", "webhook":
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
		return validateServiceConfig(kind, c)
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

// notice is a message for any channel; each kind renders it its own way. It
// is stored with each delivery to be sent again, so its fields are exported.
type notice struct {
	Event    string // firing, resolved, reminder, escalation, ack, test, digest
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
	// Key names the incident for services that track one (PagerDuty's
	// dedup_key, Opsgenie's alias): the same key opens, updates and closes it.
	Key string
	// Mention starts the message (<!here>, @oncall).
	Mention string
	// Files go with email and Telegram (the digest's PDF); the other
	// services take text only.
	Files []attachment
}

type attachment struct {
	Name, Type string
	Data       []byte
}

func (n notice) color() int {
	switch {
	case n.Event == "resolved" || n.Event == "ack":
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
	if n.Mention != "" {
		b.WriteString(n.Mention + " ")
	}
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
		title += " · " + seriesName(ev.SeriesKey, ev.SeriesLabel)
	}
	n := notice{Event: ev.State, Title: title, Severity: r.Severity, Lines: []string{ev.Message}, Runbook: r.Runbook, At: time.Now().UTC(),
		Key: alertKey(r.ID, ev.SeriesKey), Mention: r.Spec.Mention}
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
	for _, k := range sortedKeys(r.Spec.Labels) {
		n.Fields = append(n.Fields, [2]string{k, r.Spec.Labels[k]})
	}
	if ev.Explanation != "" {
		n.Note = ev.Explanation
		if !strings.HasPrefix(strings.ToLower(n.Note), "likely") {
			n.Note = "Likely cause: " + n.Note
		}
	}
	vars := templateVars(r, ev, verb)
	if r.Spec.Title != "" {
		n.Title = renderTemplate(r.Spec.Title, vars)
	}
	if r.Spec.Message != "" {
		n.Lines[0] = renderTemplate(r.Spec.Message, vars)
	}
	n.Payload = map[string]any{
		"event":       "alert." + n.Event,
		"rule":        ruleRef(r),
		"state":       ev.State,
		"series_key":  ev.SeriesKey,
		"series":      seriesName(ev.SeriesKey, ev.SeriesLabel),
		"value":       ev.Value,
		"threshold":   ev.Threshold,
		"message":     ev.Message,
		"explanation": ev.Explanation,
		"runbook":     r.Runbook,
		"event_id":    ev.ID,
		"at":          ev.CreatedAt,
		"key":         n.Key,
	}
	return n
}

// seriesName is how notices name a series: its label (a check's name)
// when it has one, else its key.
func seriesName(key, label string) string {
	if label != "" {
		return label
	}
	return key
}

// groupNotice is one message for several series of a rule that changed the
// same way in one evaluation (a host going down takes its instances with
// it), instead of one message each.
func (s *Server) groupNotice(r alertRule, evs []*alertEvent) notice {
	state := evs[0].State
	verb := map[string]string{"firing": "FIRING", "resolved": "RESOLVED"}[state]
	n := notice{Event: state, Severity: r.Severity, Runbook: r.Runbook, At: time.Now().UTC(), Mention: r.Spec.Mention,
		Title: fmt.Sprintf("[%s] %s · %d series", verb, r.Name, len(evs))}
	for _, ev := range head(evs, 15) {
		n.Lines = append(n.Lines, "• "+ev.Message)
	}
	if len(evs) > 15 {
		n.Lines = append(n.Lines, fmt.Sprintf("…and %d more.", len(evs)-15))
	}
	keys := make([]string, 0, len(evs))
	series := make([]any, 0, len(evs))
	for _, ev := range evs {
		keys = append(keys, seriesName(ev.SeriesKey, ev.SeriesLabel))
		series = append(series, map[string]any{"event_id": ev.ID, "series_key": ev.SeriesKey, "series": seriesName(ev.SeriesKey, ev.SeriesLabel), "value": ev.Value, "threshold": ev.Threshold, "message": ev.Message})
	}
	n.Fields = append(n.Fields, [2]string{"Severity", r.Severity}, [2]string{"Series", strconv.Itoa(len(evs))})
	for _, k := range sortedKeys(r.Spec.Labels) {
		n.Fields = append(n.Fields, [2]string{k, r.Spec.Labels[k]})
	}
	if ex := evs[0].Explanation; ex != "" {
		n.Note = ex
		if !strings.HasPrefix(strings.ToLower(n.Note), "likely") {
			n.Note = "Likely cause: " + n.Note
		}
	}
	if r.Spec.Title != "" {
		n.Title = renderTemplate(r.Spec.Title, templateVars(r, &alertEvent{State: state, SeriesKey: strings.Join(keys, ", "), Message: n.Lines[0]}, verb))
	}
	// Paging services track one incident per key; the group uses the rule's,
	// so it opens and closes as one.
	n.Key = alertKey(r.ID, "")
	n.Payload = map[string]any{
		"event":  "alert." + state,
		"rule":   ruleRef(r),
		"state":  state,
		"series": series,
		"key":    n.Key,
		"at":     evs[0].CreatedAt,
	}
	return n
}

// followUpNotice is an escalation or an acknowledgement of a firing series.
func (s *Server) followUpNotice(r alertRule, key, label, event, line string) notice {
	verb := map[string]string{"escalation": "ESCALATED", "ack": "ACKNOWLEDGED"}[event]
	title := fmt.Sprintf("[%s] %s", verb, r.Name)
	if key != "" {
		title += " · " + seriesName(key, label)
	}
	n := notice{Event: event, Title: title, Severity: r.Severity, Lines: []string{line}, Runbook: r.Runbook, At: time.Now().UTC(),
		Key: alertKey(r.ID, key), Fields: [][2]string{{"Severity", r.Severity}}}
	if event == "escalation" {
		n.Mention = r.Spec.Mention
	}
	n.Payload = map[string]any{
		"event":      "alert." + event,
		"rule":       ruleRef(r),
		"series_key": key,
		"message":    line,
		"key":        n.Key,
		"at":         n.At.Format(tsLayout),
	}
	return n
}

func ruleRef(r alertRule) map[string]any {
	return map[string]any{"id": r.ID, "name": r.Name, "kind": r.Kind, "severity": r.Severity, "metric": r.Spec.Metric, "labels": r.Spec.Labels}
}

// alertKey names one rule series across messages, for deduplication.
func alertKey(ruleID int64, series string) string {
	k := "visionary-rule-" + strconv.FormatInt(ruleID, 10)
	if series != "" {
		k += "-" + series
	}
	return truncate(k, 250)
}

func templateVars(r alertRule, ev *alertEvent, verb string) map[string]string {
	v := map[string]string{
		"rule": r.Name, "state": verb, "severity": r.Severity, "series": seriesName(ev.SeriesKey, ev.SeriesLabel), "message": ev.Message,
		"runbook": r.Runbook, "metric": r.Spec.Metric, "value": "", "threshold": "",
	}
	if ev.Value != nil {
		v["value"] = fnum(round2(*ev.Value))
	}
	if ev.Threshold != nil {
		v["threshold"] = fnum(round2(*ev.Threshold))
	}
	for k, val := range r.Spec.Labels {
		v["labels."+k] = val
	}
	return v
}

var templateVar = regexp.MustCompile(`\{\{\s*([a-z_]+(?:\.[A-Za-z0-9_.-]+)?)\s*\}\}`)

// renderTemplate fills {{rule}}, {{state}}, {{severity}}, {{series}},
// {{value}}, {{threshold}}, {{message}}, {{metric}}, {{runbook}} and
// {{labels.<name>}}. Unknown names stay as written, so a typo shows.
func renderTemplate(t string, vars map[string]string) string {
	return templateVar.ReplaceAllStringFunc(t, func(m string) string {
		if v, ok := vars[templateVar.FindStringSubmatch(m)[1]]; ok {
			return v
		}
		return m
	})
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func round2(v float64) float64 { return math.Round(v*100) / 100 }

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
		return s.sendEmail(ctx, c.Config.To, n.Title, n.plain(), n.HTML, n.Files...)
	case "teams":
		return postJSON(ctx, c.Config.URL, teamsBody(n), nil)
	case "google_chat":
		return postJSON(ctx, c.Config.URL, map[string]any{"text": googleChatText(n)}, nil)
	case "pagerduty":
		return sendPagerDuty(ctx, c.Config, n)
	case "opsgenie":
		return sendOpsgenie(ctx, c.Config, n)
	case "telegram":
		return sendTelegram(ctx, c.Config, n)
	case "sms":
		return sendSMS(ctx, c.Config, n)
	}
	return errors.New("unknown channel kind")
}

func slackBody(n notice) map[string]any {
	text := "*" + n.Title + "*\n" + strings.Join(n.Lines, "\n")
	if n.Mention != "" {
		text = n.Mention + " " + text
	}
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
	body := map[string]any{"username": "Visionary", "embeds": []any{embed}}
	if n.Mention != "" {
		body["content"] = truncate(n.Mention, 500)
	}
	return body
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
	headers := map[string]string{"Content-Type": "application/json"}
	if len(secret) > 0 {
		mac := hmac.New(sha256.New, secret)
		mac.Write(payload)
		headers["X-Visionary-Signature"] = "sha256=" + hex.EncodeToString(mac.Sum(nil))
	}
	return post(ctx, target, payload, headers)
}

// post sends payload with headers and treats any answer from 300 up as a
// failure.
func post(ctx context.Context, target string, payload []byte, headers map[string]string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return errors.New("invalid webhook URL")
	}
	req.Header.Set("User-Agent", UserAgent)
	for k, v := range headers {
		req.Header.Set(k, v)
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
