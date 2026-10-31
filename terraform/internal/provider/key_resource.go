package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type apiKeyResource struct{ base }

func newAPIKeyResource() resource.Resource { return &apiKeyResource{} }

var _ resource.ResourceWithImportState = (*apiKeyResource)(nil)

type keyModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Scope       types.String `tfsdk:"scope"`
	ExpiresDays types.Int64  `tfsdk:"expires_days"`
	Secret      types.String `tfsdk:"secret"`
	Prefix      types.String `tfsdk:"prefix"`
	CreatedAt   types.String `tfsdk:"created_at"`
	ExpiresAt   types.String `tfsdk:"expires_at"`
}

type apiKey struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Scope     string  `json:"scope"`
	Prefix    string  `json:"prefix"`
	CreatedAt string  `json:"created_at"`
	ExpiresAt *string `json:"expires_at"`
	RevokedAt *string `json:"revoked_at"`
}

func (r *apiKeyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_api_key"
}

func (r *apiKeyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		Description: "An API key. Visionary shows the key once, when it is made, so it is kept in the state as secret; " +
			"keys can't be changed, so any change makes a new key and revokes the old one. Needs an admin key.",
		Attributes: map[string]schema.Attribute{
			"id":   idAttribute(),
			"name": schema.StringAttribute{Description: "Name.", Required: true, PlanModifiers: replace, Validators: []validator.String{stringvalidator.LengthBetween(1, 80)}},
			"scope": schema.StringAttribute{
				Description: "read (the HTTP API as a viewer), editor or admin (the HTTP API with that role), server (SDKs, OTLP, the host agent), client (browsers and apps), scim, or ci (runs uptime checks and reports Playwright tests only).",
				Required:    true, PlanModifiers: replace,
				Validators: []validator.String{stringvalidator.OneOf("read", "editor", "admin", "server", "client", "scim", "ci")},
			},
			"expires_days": schema.Int64Attribute{
				Description: "Days until it expires; leave out for never.", Optional: true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.RequiresReplace()}, Validators: []validator.Int64{int64validator.Between(1, 3650)},
			},
			"secret":     schema.StringAttribute{Description: "The key, vis_…. Empty for an imported key.", Computed: true, Sensitive: true, PlanModifiers: keep},
			"prefix":     schema.StringAttribute{Description: "The visible start of the key.", Computed: true, PlanModifiers: keep},
			"created_at": schema.StringAttribute{Description: "When it was made.", Computed: true, PlanModifiers: keep},
			"expires_at": schema.StringAttribute{Description: "When it expires, if it does.", Computed: true, PlanModifiers: keep},
		},
	}
}

func (m *keyModel) fill(k apiKey) {
	m.ID, m.Name, m.Scope, m.Prefix, m.CreatedAt = idString(k.ID), types.StringValue(k.Name), types.StringValue(k.Scope), types.StringValue(k.Prefix), types.StringValue(k.CreatedAt)
	m.ExpiresAt = types.StringNull()
	if k.ExpiresAt != nil {
		m.ExpiresAt = types.StringValue(*k.ExpiresAt)
	}
}

func (r *apiKeyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan keyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := map[string]any{"name": plan.Name.ValueString(), "scope": plan.Scope.ValueString(), "expires_days": plan.ExpiresDays.ValueInt64()}
	var out struct {
		Key    apiKey `json:"key"`
		Secret string `json:"secret"`
	}
	if err := r.c.do(ctx, http.MethodPost, "/api/team/keys", in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "create API key", err)
		return
	}
	plan.fill(out.Key)
	plan.Secret = types.StringValue(out.Secret)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state keyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiKey
	err := r.c.do(ctx, http.MethodGet, "/api/team/keys/"+state.ID.ValueString(), nil, &out)
	if isNotFound(err) || err == nil && out.RevokedAt != nil {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiag(&resp.Diagnostics, "read API key", err)
		return
	}
	state.fill(out)
	if state.Secret.IsNull() || state.Secret.IsUnknown() {
		state.Secret = types.StringValue("")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: every setting replaces the key
func (r *apiKeyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan keyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *apiKeyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state keyModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.c.do(ctx, http.MethodDelete, "/api/team/keys/"+state.ID.ValueString(), nil, nil)
	if err != nil && !isNotFound(err) {
		apiDiag(&resp.Diagnostics, "revoke API key", err)
	}
}
