package provider

import (
	"context"
	"net/url"
	"regexp"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
)

var clockRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

// tagRe matches an uptime check tag as the server stores it.
var tagRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_.:-]{0,39}$`)

type rfc3339Validator struct{}

func (rfc3339Validator) Description(context.Context) string { return "must be an RFC 3339 time" }
func (v rfc3339Validator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (rfc3339Validator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := time.Parse(time.RFC3339, req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid time", "Use RFC 3339, such as 2026-11-01T22:00:00Z.")
	}
}

type httpURLValidator struct{}

func (httpURLValidator) Description(context.Context) string {
	return "must be an absolute http:// or https:// URL"
}
func (v httpURLValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (httpURLValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	u, err := url.Parse(req.ConfigValue.ValueString())
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid URL", "Use an absolute http:// or https:// address.")
	}
}

// jsonPanelsValidator checks a dashboard's panels JSON parses as panels.
type jsonPanelsValidator struct{}

func (jsonPanelsValidator) Description(context.Context) string {
	return "must be a JSON array of dashboard panels"
}
func (v jsonPanelsValidator) MarkdownDescription(ctx context.Context) string {
	return v.Description(ctx)
}

func (jsonPanelsValidator) ValidateString(_ context.Context, req validator.StringRequest, resp *validator.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	if _, err := parsePanels(req.ConfigValue.ValueString()); err != nil {
		resp.Diagnostics.AddAttributeError(req.Path, "Invalid panels", err.Error())
	}
}
