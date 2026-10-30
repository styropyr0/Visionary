package provider

import (
	"context"
	"net/http"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var (
	channelKinds = []string{"slack", "discord", "teams", "google_chat", "webhook", "email", "pagerduty", "opsgenie", "telegram", "sms"}
	urlKinds     = []string{"slack", "discord", "teams", "google_chat", "webhook"}
	secretKinds  = []string{"webhook", "pagerduty", "opsgenie", "telegram", "sms"}
	// Paging services and SMS get alerts only, not the weekly digest.
	noDigestKinds = []string{"pagerduty", "opsgenie", "sms"}
)

type alertChannelResource struct{ base }

func newAlertChannelResource() resource.Resource { return &alertChannelResource{} }

var (
	_ resource.ResourceWithImportState    = (*alertChannelResource)(nil)
	_ resource.ResourceWithValidateConfig = (*alertChannelResource)(nil)
)

type channelModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Kind          types.String `tfsdk:"kind"`
	URL           types.String `tfsdk:"url"`
	Secret        types.String `tfsdk:"secret"`
	To            types.List   `tfsdk:"to"`
	ChatID        types.String `tfsdk:"chat_id"`
	AccountSID    types.String `tfsdk:"account_sid"`
	From          types.String `tfsdk:"from"`
	Region        types.String `tfsdk:"region"`
	DigestEnabled types.Bool   `tfsdk:"digest_enabled"`
	DigestWeekday types.Int64  `tfsdk:"digest_weekday"`
	DigestHour    types.Int64  `tfsdk:"digest_hour"`
	URLMasked     types.String `tfsdk:"url_masked"`
	HasSecret     types.Bool   `tfsdk:"has_secret"`
}

// apiChannel is a channel as the API shows it: no URL or secret in the clear.
type apiChannel struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	URLMasked     string   `json:"url_masked"`
	HasSecret     bool     `json:"has_secret"`
	To            []string `json:"to"`
	ChatID        string   `json:"chat_id"`
	AccountSID    string   `json:"account_sid"`
	From          string   `json:"from"`
	Region        string   `json:"region"`
	DigestEnabled bool     `json:"digest_enabled"`
	DigestWeekday int64    `json:"digest_weekday"`
	DigestHour    int64    `json:"digest_hour"`
}

type channelInput struct {
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	URL           string   `json:"url,omitempty"`
	Secret        string   `json:"secret,omitempty"`
	ClearSecret   bool     `json:"clear_secret,omitempty"`
	To            []string `json:"to"`
	ChatID        string   `json:"chat_id,omitempty"`
	AccountSID    string   `json:"account_sid,omitempty"`
	From          string   `json:"from,omitempty"`
	Region        string   `json:"region,omitempty"`
	DigestEnabled bool     `json:"digest_enabled"`
	DigestWeekday *int64   `json:"digest_weekday"`
	DigestHour    *int64   `json:"digest_hour"`
}

func (r *alertChannelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_channel"
}

func (r *alertChannelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	nonEmpty := []validator.String{stringvalidator.LengthAtLeast(1)}
	resp.Schema = schema.Schema{
		Description: "A notification channel alerts are sent to: Slack, Discord, Teams, Google Chat, a webhook, email, PagerDuty, Opsgenie, Telegram or SMS. " +
			"Visionary never shows a channel's URL or secret again, so changes made to them outside Terraform are only noticed when one is removed.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Description: "Name, as rules and exports refer to it.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 120)}},
			"kind": schema.StringAttribute{
				Description:   "One of slack, discord, teams, google_chat, webhook, email, pagerduty, opsgenie, telegram, sms. Changing it replaces the channel.",
				Required:      true,
				Validators:    []validator.String{stringvalidator.OneOf(channelKinds...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"url": schema.StringAttribute{Description: "Webhook URL (slack, discord, teams, google_chat, webhook).", Optional: true, Sensitive: true, Validators: nonEmpty},
			"secret": schema.StringAttribute{
				Description: "Webhook signing secret, PagerDuty routing key, Opsgenie API key, Telegram bot token or Twilio auth token.",
				Optional:    true, Sensitive: true, Validators: nonEmpty,
			},
			"to": schema.ListAttribute{
				Description: "Email addresses (email) or phone numbers in international form (sms).",
				Optional:    true, ElementType: types.StringType, Validators: []validator.List{listvalidator.SizeBetween(1, 20)},
			},
			"chat_id":     schema.StringAttribute{Description: "Telegram chat id or @channel.", Optional: true, Validators: nonEmpty},
			"account_sid": schema.StringAttribute{Description: "Twilio account SID (sms).", Optional: true, Validators: nonEmpty},
			"from":        schema.StringAttribute{Description: "Twilio sender number (sms).", Optional: true, Validators: nonEmpty},
			"region":      schema.StringAttribute{Description: "Opsgenie region: us or eu.", Optional: true, Validators: []validator.String{stringvalidator.OneOf("us", "eu")}},
			"digest_enabled": schema.BoolAttribute{
				Description: "Send the weekly digest here (chat, webhook and email channels).",
				Optional:    true, Computed: true, Default: booldefault.StaticBool(false),
			},
			"digest_weekday": schema.Int64Attribute{
				Description: "Digest day, 0 (Sunday) to 6.", Optional: true, Computed: true, Default: int64default.StaticInt64(1),
				Validators: []validator.Int64{int64validator.Between(0, 6)},
			},
			"digest_hour": schema.Int64Attribute{
				Description: "Digest hour, 0 to 23, in the digest time zone.", Optional: true, Computed: true, Default: int64default.StaticInt64(9),
				Validators: []validator.Int64{int64validator.Between(0, 23)},
			},
			"url_masked": schema.StringAttribute{Description: "The URL as Visionary shows it, with its token hidden.", Computed: true,
				PlanModifiers: []planmodifier.String{keepIf("url")}},
			"has_secret": schema.BoolAttribute{Description: "Whether a secret is stored.", Computed: true,
				PlanModifiers: []planmodifier.Bool{keepIf("secret")}},
		},
	}
}

func (r *alertChannelResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m channelModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Kind.IsUnknown() || m.Kind.IsNull() {
		return
	}
	kind := m.Kind.ValueString()
	need := func(attr string, v interface{ IsNull() bool }, when bool, why string) {
		if when && v.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root(attr), "Missing "+attr, why)
		}
		if !when && !v.IsNull() {
			resp.Diagnostics.AddAttributeError(path.Root(attr), "Unused "+attr, kind+" channels don't use "+attr+".")
		}
	}
	need("url", m.URL, slices.Contains(urlKinds, kind), kind+" channels need the webhook URL.")
	if !slices.Contains(secretKinds, kind) {
		need("secret", m.Secret, false, "")
	} else if kind != "webhook" {
		need("secret", m.Secret, true, kind+" channels need their key or token in secret.")
	}
	need("to", m.To, kind == "email" || kind == "sms", kind+" channels need recipients in to.")
	need("chat_id", m.ChatID, kind == "telegram", "telegram channels need chat_id.")
	need("account_sid", m.AccountSID, kind == "sms", "sms channels need the Twilio account_sid.")
	need("from", m.From, kind == "sms", "sms channels need the sender number in from.")
	if kind != "opsgenie" && !m.Region.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("region"), "Unused region", "Only opsgenie channels have a region.")
	}
	if slices.Contains(noDigestKinds, kind) && m.DigestEnabled.ValueBool() {
		resp.Diagnostics.AddAttributeError(path.Root("digest_enabled"), "No digest here",
			"Weekly digests go to chat, webhook or email channels, not paging or SMS.")
	}
}

func (m *channelModel) input(ctx context.Context, prior *channelModel, diags *diag.Diagnostics) channelInput {
	in := channelInput{
		Name: m.Name.ValueString(), Kind: m.Kind.ValueString(), URL: m.URL.ValueString(), Secret: m.Secret.ValueString(),
		To: stringsOf(ctx, m.To, diags), ChatID: m.ChatID.ValueString(), AccountSID: m.AccountSID.ValueString(),
		From: m.From.ValueString(), Region: m.Region.ValueString(), DigestEnabled: m.DigestEnabled.ValueBool(),
	}
	wd, hr := m.DigestWeekday.ValueInt64(), m.DigestHour.ValueInt64()
	in.DigestWeekday, in.DigestHour = &wd, &hr
	if prior != nil && m.Secret.IsNull() && !prior.Secret.IsNull() {
		in.ClearSecret = true
	}
	if in.To == nil {
		in.To = []string{}
	}
	return in
}

// fill copies the server's view into the model. The URL and secret are
// never shown, so they keep their values unless the server has none.
func (m *channelModel) fill(c apiChannel) {
	m.ID = idString(c.ID)
	m.Name, m.Kind = types.StringValue(c.Name), types.StringValue(c.Kind)
	m.To = optStringList(c.To, m.To)
	m.ChatID, m.AccountSID = optString(c.ChatID, m.ChatID), optString(c.AccountSID, m.AccountSID)
	m.From, m.Region = optString(c.From, m.From), optString(c.Region, m.Region)
	m.DigestEnabled, m.DigestWeekday, m.DigestHour = types.BoolValue(c.DigestEnabled), types.Int64Value(c.DigestWeekday), types.Int64Value(c.DigestHour)
	m.URLMasked, m.HasSecret = types.StringValue(c.URLMasked), types.BoolValue(c.HasSecret)
	if c.URLMasked == "" {
		m.URL = types.StringNull()
	}
	if !c.HasSecret {
		m.Secret = types.StringNull()
	}
}

func (r *alertChannelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan channelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(ctx, nil, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiChannel
	if err := r.c.do(ctx, http.MethodPost, "/api/alerts/channels", in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "create channel", err)
		return
	}
	plan.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertChannelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state channelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiChannel
	err := r.c.do(ctx, http.MethodGet, "/api/alerts/channels/"+state.ID.ValueString(), nil, &out)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiag(&resp.Diagnostics, "read channel", err)
		return
	}
	state.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *alertChannelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior channelModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(ctx, &prior, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiChannel
	if err := r.c.do(ctx, http.MethodPut, "/api/alerts/channels/"+prior.ID.ValueString(), in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "update channel", err)
		return
	}
	plan.fill(out)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *alertChannelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state channelModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.c.do(ctx, http.MethodDelete, "/api/alerts/channels/"+state.ID.ValueString(), nil, nil)
	if err != nil && !isNotFound(err) {
		apiDiag(&resp.Diagnostics, "delete channel", err)
	}
}
