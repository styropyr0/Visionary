package analytics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Channels for services with their own API: PagerDuty (Events API v2) and
// Opsgenie, which keep one incident per rule series and close it when the
// alert resolves; Microsoft Teams and Google Chat incoming webhooks;
// Telegram bots; SMS through Twilio. The endpoints are variables so tests
// can point them at a local server.

var (
	pagerDutyURL = "https://events.pagerduty.com/v2/enqueue"
	opsgenieURL  = map[string]string{"us": "https://api.opsgenie.com", "eu": "https://api.eu.opsgenie.com"}
	telegramAPI  = "https://api.telegram.org"
	twilioAPI    = "https://api.twilio.com"
)

var (
	telegramToken = regexp.MustCompile(`^\d{3,20}:[A-Za-z0-9_-]{20,100}$`)
	telegramChat  = regexp.MustCompile(`^(-?\d{1,20}|@[A-Za-z0-9_]{4,64})$`)
	twilioSID     = regexp.MustCompile(`^AC[0-9a-fA-F]{32}$`)
	phoneNumber   = regexp.MustCompile(`^\+[1-9]\d{6,14}$`)
)

func validateServiceConfig(kind string, c channelConfig) error {
	switch kind {
	case "pagerduty":
		if len(c.Secret) < 20 || len(c.Secret) > 64 {
			return errors.New("enter the integration's routing key (Events API v2)")
		}
	case "opsgenie":
		if len(c.Secret) < 20 || len(c.Secret) > 128 {
			return errors.New("enter an Opsgenie API integration key")
		}
		if _, ok := opsgenieURL[fallback(c.Region, "us")]; !ok {
			return errors.New("region must be us or eu")
		}
	case "telegram":
		if !telegramToken.MatchString(c.Secret) {
			return errors.New("enter the bot token from @BotFather (123456:ABC…)")
		}
		if !telegramChat.MatchString(c.ChatID) {
			return errors.New("enter the chat ID (a number, negative for groups) or @channelname")
		}
	case "sms":
		if !twilioSID.MatchString(c.AccountSID) {
			return errors.New("enter the Twilio account SID (AC followed by 32 characters)")
		}
		if len(c.Secret) < 16 {
			return errors.New("enter the Twilio auth token")
		}
		if !phoneNumber.MatchString(c.From) {
			return errors.New("the sender must be a phone number in international form, like +14155550100")
		}
		if len(c.To) == 0 || len(c.To) > 10 {
			return errors.New("enter 1 to 10 phone numbers")
		}
		for _, to := range c.To {
			if !phoneNumber.MatchString(to) {
				return fmt.Errorf("%q is not a phone number in international form (+14155550100)", to)
			}
		}
	default:
		return errors.New("unknown channel kind")
	}
	return nil
}

// pagerDutySeverity maps a rule's severity onto PagerDuty's.
var pagerDutySeverity = map[string]string{"critical": "critical", "warning": "warning", "info": "info"}

func sendPagerDuty(ctx context.Context, c channelConfig, n notice) error {
	action := "trigger"
	switch n.Event {
	case "resolved":
		action = "resolve"
	case "ack":
		action = "acknowledge"
	}
	key := fallback(n.Key, "visionary-"+n.Event)
	body := map[string]any{"routing_key": c.Secret, "event_action": action, "dedup_key": key}
	if action == "trigger" {
		body["payload"] = map[string]any{
			"summary":        truncate(n.Title, 1000),
			"source":         "visionary",
			"severity":       fallback(pagerDutySeverity[n.Severity], "warning"),
			"timestamp":      n.At.Format("2006-01-02T15:04:05Z07:00"),
			"custom_details": noticeDetails(n),
		}
	}
	if err := postJSON(ctx, pagerDutyURL, body, nil); err != nil {
		return err
	}
	// A test opens an incident to prove the key works; close it again.
	if n.Event == "test" {
		return postJSON(ctx, pagerDutyURL, map[string]any{"routing_key": c.Secret, "event_action": "resolve", "dedup_key": key}, nil)
	}
	return nil
}

var opsgeniePriority = map[string]string{"critical": "P1", "warning": "P3", "info": "P5"}

func sendOpsgenie(ctx context.Context, c channelConfig, n notice) error {
	base := opsgenieURL[fallback(c.Region, "us")]
	headers := map[string]string{"Content-Type": "application/json", "Authorization": "GenieKey " + c.Secret}
	alias := truncate(fallback(n.Key, "visionary-"+n.Event), 500)
	send := func(path string, body any) error {
		payload, _ := json.Marshal(body)
		return post(ctx, base+path, payload, headers)
	}
	switch n.Event {
	case "resolved":
		return send("/v2/alerts/"+url.PathEscape(alias)+"/close?identifierType=alias", map[string]any{"source": "visionary", "note": truncate(strings.Join(n.Lines, "\n"), 1000)})
	case "ack":
		return send("/v2/alerts/"+url.PathEscape(alias)+"/acknowledge?identifierType=alias", map[string]any{"source": "visionary", "note": truncate(strings.Join(n.Lines, "\n"), 1000)})
	}
	details := map[string]string{}
	for _, f := range n.Fields {
		details[f[0]] = truncate(f[1], 1000)
	}
	desc := strings.Join(n.Lines, "\n")
	if n.Note != "" {
		desc += "\n\n" + n.Note
	}
	if n.Runbook != "" {
		desc += "\n\nRunbook: " + n.Runbook
	}
	err := send("/v2/alerts", map[string]any{
		"message":     truncate(n.Title, 128),
		"alias":       alias,
		"description": truncate(desc, 15000),
		"priority":    fallback(opsgeniePriority[n.Severity], "P3"),
		"source":      "visionary",
		"details":     details,
	})
	if err == nil && n.Event == "test" {
		return send("/v2/alerts/"+url.PathEscape(alias)+"/close?identifierType=alias", map[string]any{"source": "visionary", "note": "Test finished."})
	}
	return err
}

func sendTelegram(ctx context.Context, c channelConfig, n notice) error {
	return postJSON(ctx, telegramAPI+"/bot"+c.Secret+"/sendMessage", map[string]any{
		"chat_id":                  c.ChatID,
		"text":                     truncate(n.plain(), 4000),
		"disable_web_page_preview": true,
	}, nil)
}

// sendSMS texts every number; it fails if any number fails, naming it, so a
// retry tries them all again.
func sendSMS(ctx context.Context, c channelConfig, n notice) error {
	text := n.Title
	if len(n.Lines) > 0 {
		text += "\n" + n.Lines[0]
	}
	text = truncate(text, 300)
	target := twilioAPI + "/2010-04-01/Accounts/" + url.PathEscape(c.AccountSID) + "/Messages.json"
	headers := map[string]string{
		"Content-Type":  "application/x-www-form-urlencoded",
		"Authorization": "Basic " + base64.StdEncoding.EncodeToString([]byte(c.AccountSID+":"+c.Secret)),
	}
	var failed []string
	var last error
	for _, to := range c.To {
		form := url.Values{"To": {to}, "From": {c.From}, "Body": {text}}
		if err := post(ctx, target, []byte(form.Encode()), headers); err != nil {
			failed, last = append(failed, to), err
		}
	}
	if last != nil {
		return fmt.Errorf("%s: %w", strings.Join(failed, ", "), last)
	}
	return nil
}

func teamsBody(n notice) map[string]any {
	title := n.Title
	if n.Mention != "" {
		title = n.Mention + " " + title
	}
	color := map[string]string{"critical": "Attention", "warning": "Warning"}[n.Severity]
	if n.Event == "resolved" || n.Event == "ack" {
		color = "Good"
	}
	body := []any{map[string]any{"type": "TextBlock", "text": truncate(title, 500), "weight": "Bolder", "size": "Medium", "wrap": true, "color": fallback(color, "Default")}}
	for _, l := range head(n.Lines, 20) {
		body = append(body, map[string]any{"type": "TextBlock", "text": truncate(l, 2000), "wrap": true})
	}
	if len(n.Fields) > 0 {
		var facts []any
		for _, f := range head(n.Fields, 15) {
			facts = append(facts, map[string]string{"title": f[0], "value": truncate(f[1], 500)})
		}
		body = append(body, map[string]any{"type": "FactSet", "facts": facts})
	}
	if n.Note != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": truncate(n.Note, 3000), "wrap": true, "isSubtle": true})
	}
	if n.Runbook != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": "Runbook: " + truncate(n.Runbook, 2000), "wrap": true, "isSubtle": true})
	}
	return map[string]any{
		"type": "message",
		"attachments": []any{map[string]any{
			"contentType": "application/vnd.microsoft.card.adaptive",
			"content": map[string]any{
				"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
				"type":    "AdaptiveCard",
				"version": "1.4",
				"body":    body,
			},
		}},
	}
}

func googleChatText(n notice) string {
	var b strings.Builder
	if n.Mention != "" {
		b.WriteString(n.Mention + " ")
	}
	b.WriteString("*" + n.Title + "*\n")
	for _, l := range n.Lines {
		b.WriteString(l + "\n")
	}
	for _, f := range n.Fields {
		b.WriteString("_" + f[0] + "_: " + f[1] + "\n")
	}
	if n.Note != "" {
		b.WriteString("\n" + n.Note + "\n")
	}
	if n.Runbook != "" {
		b.WriteString("\nRunbook: " + n.Runbook + "\n")
	}
	return truncate(b.String(), 4000)
}

func noticeDetails(n notice) map[string]any {
	d := map[string]any{"message": strings.Join(n.Lines, "\n")}
	for _, f := range n.Fields {
		d[f[0]] = f[1]
	}
	if n.Note != "" {
		d["likely_cause"] = n.Note
	}
	if n.Runbook != "" {
		d["runbook"] = n.Runbook
	}
	return d
}
