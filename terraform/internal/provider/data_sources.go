package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type dsBase struct{ c *client }

func (b *dsBase) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	b.c = c
}

type servicesDataSource struct{ dsBase }

func newServicesDataSource() datasource.DataSource { return &servicesDataSource{} }

type servicesModel struct {
	ID       types.String `tfsdk:"id"`
	Services types.List   `tfsdk:"services"`
}

func (d *servicesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_services"
}

func (d *servicesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "The services that reported to Visionary recently, for example to make one rule per service with for_each.",
		Attributes: map[string]schema.Attribute{
			"id":       schema.StringAttribute{Computed: true, Description: "Always services."},
			"services": schema.ListAttribute{Computed: true, ElementType: types.StringType, Description: "Service names."},
		},
	}
}

func (d *servicesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	var out struct {
		Services []string `json:"services"`
	}
	if err := d.c.do(ctx, http.MethodGet, "/api/alerts/meta", nil, &out); err != nil {
		resp.Diagnostics.AddError("Visionary: list services", err.Error())
		return
	}
	m := servicesModel{ID: types.StringValue("services"), Services: optStringList(append([]string{}, out.Services...), types.ListUnknown(types.StringType))}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}

type alertChannelDataSource struct{ dsBase }

func newAlertChannelDataSource() datasource.DataSource { return &alertChannelDataSource{} }

type channelLookupModel struct {
	ID            types.String `tfsdk:"id"`
	Name          types.String `tfsdk:"name"`
	Kind          types.String `tfsdk:"kind"`
	URLMasked     types.String `tfsdk:"url_masked"`
	HasSecret     types.Bool   `tfsdk:"has_secret"`
	DigestEnabled types.Bool   `tfsdk:"digest_enabled"`
}

func (d *alertChannelDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_alert_channel"
}

func (d *alertChannelDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "Looks up a notification channel made outside Terraform, by name or id, so rules can send to it.",
		Attributes: map[string]schema.Attribute{
			"id":             schema.StringAttribute{Optional: true, Computed: true, Description: "Channel id; give this or name."},
			"name":           schema.StringAttribute{Optional: true, Computed: true, Description: "Channel name; give this or id."},
			"kind":           schema.StringAttribute{Computed: true, Description: "Channel kind."},
			"url_masked":     schema.StringAttribute{Computed: true, Description: "The URL with its token hidden."},
			"has_secret":     schema.BoolAttribute{Computed: true, Description: "Whether a secret is stored."},
			"digest_enabled": schema.BoolAttribute{Computed: true, Description: "Whether the weekly digest goes here."},
		},
	}
}

func (d *alertChannelDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var m channelLookupModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.ID.IsNull() == m.Name.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Give name or id", "Look the channel up by exactly one of name and id.")
		return
	}
	var out struct {
		Channels []apiChannel `json:"channels"`
	}
	if err := d.c.do(ctx, http.MethodGet, "/api/alerts/channels", nil, &out); err != nil {
		resp.Diagnostics.AddError("Visionary: list channels", err.Error())
		return
	}
	for _, c := range out.Channels {
		if (!m.Name.IsNull() && c.Name == m.Name.ValueString()) || (!m.ID.IsNull() && idString(c.ID).Equal(m.ID)) {
			m = channelLookupModel{ID: idString(c.ID), Name: types.StringValue(c.Name), Kind: types.StringValue(c.Kind),
				URLMasked: types.StringValue(c.URLMasked), HasSecret: types.BoolValue(c.HasSecret), DigestEnabled: types.BoolValue(c.DigestEnabled)}
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			return
		}
	}
	resp.Diagnostics.AddError("Channel not found", "No notification channel is named "+m.Name.ValueString()+m.ID.ValueString()+".")
}
