package provider

import (
	"context"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type dashboardResource struct{ base }

func newDashboardResource() resource.Resource { return &dashboardResource{} }

var (
	_ resource.ResourceWithImportState    = (*dashboardResource)(nil)
	_ resource.ResourceWithValidateConfig = (*dashboardResource)(nil)
)

type dashboardModel struct {
	ID          types.String   `tfsdk:"id"`
	Name        types.String   `tfsdk:"name"`
	Description types.String   `tfsdk:"description"`
	Panels      panelsValue    `tfsdk:"panels"`
	Variables   []dashVarModel `tfsdk:"variables"`
	Time        *dashTimeModel `tfsdk:"time"`
}

type dashVarModel struct {
	Name    types.String `tfsdk:"name"`
	Default types.String `tfsdk:"default"`
}

type dashTimeModel struct {
	Kind types.String `tfsdk:"kind"`
	Key  types.String `tfsdk:"key"`
	From types.String `tfsdk:"from"`
	To   types.String `tfsdk:"to"`
}

type apiDashboard struct {
	ID          int64     `json:"id,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Layout      apiLayout `json:"layout"`
}

type apiLayout struct {
	Panels    []panel      `json:"panels"`
	Variables []apiDashVar `json:"variables"`
	Time      *apiDashTime `json:"time,omitempty"`
}

type apiDashVar struct {
	Name    string `json:"name"`
	Default string `json:"default,omitempty"`
}

type apiDashTime struct {
	Kind string `json:"kind"`
	Key  string `json:"key,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

func (r *dashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *dashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		Description: "A custom dashboard: saved and built-in panels on a 12-column grid, with optional variables and its own time range.",
		Attributes: map[string]schema.Attribute{
			"id":          idAttribute(),
			"name":        schema.StringAttribute{Description: "Name.", Required: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 120)}},
			"description": schema.StringAttribute{Description: "Description.", Optional: true, Validators: []validator.String{stringvalidator.LengthBetween(1, 500)}},
			"panels": schema.StringAttribute{
				Description: "The panels as a JSON array, best written with jsonencode(). Each has id, kind (saved or builtin), ref " +
					"(a saved panel id, or a built-in chart such as overview/latency), x, y, w, h on the 12-column grid, and optionally " +
					"title, description, params, field and viz. Formatting and empty fields don't count as changes.",
				Required:   true,
				CustomType: panelsType{},
				Validators: []validator.String{jsonPanelsValidator{}},
			},
			"variables": schema.ListNestedAttribute{
				Description: "Variables shown above the panels.",
				Optional:    true,
				Validators:  []validator.List{listvalidator.SizeBetween(1, 3)},
				NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
					"name":    schema.StringAttribute{Description: "instance, route or service.", Required: true, Validators: []validator.String{stringvalidator.OneOf("instance", "route", "service")}},
					"default": schema.StringAttribute{Description: "Starting value.", Optional: true, Validators: []validator.String{stringvalidator.LengthAtLeast(1)}},
				}},
			},
			"time": schema.SingleNestedAttribute{
				Description: "A time range of its own instead of the global one.",
				Optional:    true,
				Attributes: map[string]schema.Attribute{
					"kind": schema.StringAttribute{Description: "relative or absolute.", Required: true, Validators: []validator.String{stringvalidator.OneOf("relative", "absolute")}},
					"key":  schema.StringAttribute{Description: "Relative range: 15m, 1h, 6h, 24h, 7d or 30d.", Optional: true, Validators: []validator.String{stringvalidator.OneOf("15m", "1h", "6h", "24h", "7d", "30d")}},
					"from": schema.StringAttribute{Description: "Absolute range start, RFC 3339.", Optional: true, Validators: []validator.String{rfc3339Validator{}}},
					"to":   schema.StringAttribute{Description: "Absolute range end, RFC 3339.", Optional: true, Validators: []validator.String{rfc3339Validator{}}},
				},
			},
		},
	}
}

func (r *dashboardResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var kind, key, from, to types.String
	t := path.Root("time")
	for p, v := range map[string]*types.String{"kind": &kind, "key": &key, "from": &from, "to": &to} {
		resp.Diagnostics.Append(req.Config.GetAttribute(ctx, t.AtName(p), v)...)
	}
	if resp.Diagnostics.HasError() || kind.IsNull() || kind.IsUnknown() {
		return
	}
	if kind.ValueString() == "relative" && (key.IsNull() || !from.IsNull() || !to.IsNull()) {
		resp.Diagnostics.AddAttributeError(t, "Invalid time", "A relative range takes key only.")
	}
	if kind.ValueString() == "absolute" && (!key.IsNull() || from.IsNull() || to.IsNull()) {
		resp.Diagnostics.AddAttributeError(t, "Invalid time", "An absolute range takes from and to only.")
	}
}

func (m *dashboardModel) input(diags *diag.Diagnostics) apiDashboard {
	panels, err := parsePanels(m.Panels.ValueString())
	if err != nil {
		diags.AddAttributeError(path.Root("panels"), "Invalid panels", err.Error())
	}
	d := apiDashboard{Name: m.Name.ValueString(), Description: m.Description.ValueString(),
		Layout: apiLayout{Panels: panels, Variables: []apiDashVar{}}}
	for _, v := range m.Variables {
		d.Layout.Variables = append(d.Layout.Variables, apiDashVar{Name: v.Name.ValueString(), Default: v.Default.ValueString()})
	}
	if t := m.Time; t != nil {
		d.Layout.Time = &apiDashTime{Kind: t.Kind.ValueString(), Key: t.Key.ValueString(), From: t.From.ValueString(), To: t.To.ValueString()}
	}
	return d
}

func (m *dashboardModel) fill(d apiDashboard, diags *diag.Diagnostics) {
	m.ID, m.Name, m.Description = idString(d.ID), types.StringValue(d.Name), optString(d.Description, m.Description)
	if d.Layout.Panels == nil {
		d.Layout.Panels = []panel{}
	}
	s, err := canonicalPanels(d.Layout.Panels)
	if err != nil {
		diags.AddError("Unexpected dashboard panels", err.Error())
		return
	}
	m.Panels = panelsOf(s)
	if len(d.Layout.Variables) == 0 && m.Variables == nil {
		m.Variables = nil
	} else {
		prev := m.Variables
		m.Variables = make([]dashVarModel, len(d.Layout.Variables))
		for i, v := range d.Layout.Variables {
			ref := types.StringNull()
			if i < len(prev) {
				ref = prev[i].Default
			}
			m.Variables[i] = dashVarModel{Name: types.StringValue(v.Name), Default: optString(v.Default, ref)}
		}
	}
	if t := d.Layout.Time; t == nil {
		m.Time = nil
	} else {
		m.Time = &dashTimeModel{Kind: types.StringValue(t.Kind), Key: optString(t.Key, types.StringNull()),
			From: optString(t.From, types.StringNull()), To: optString(t.To, types.StringNull())}
	}
}

func (r *dashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiDashboard
	if err := r.c.do(ctx, http.MethodPost, "/api/dashboards", in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "create dashboard", err)
		return
	}
	plan.fill(out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiDashboard
	err := r.c.do(ctx, http.MethodGet, "/api/dashboards/"+state.ID.ValueString(), nil, &out)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		apiDiag(&resp.Diagnostics, "read dashboard", err)
		return
	}
	state.fill(out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior dashboardModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	in := plan.input(&resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	var out apiDashboard
	if err := r.c.do(ctx, http.MethodPut, "/api/dashboards/"+prior.ID.ValueString(), in, &out); err != nil {
		apiDiag(&resp.Diagnostics, "update dashboard", err)
		return
	}
	plan.fill(out, &resp.Diagnostics)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dashboardModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.c.do(ctx, http.MethodDelete, "/api/dashboards/"+state.ID.ValueString(), nil, nil)
	if err != nil && !isNotFound(err) {
		apiDiag(&resp.Diagnostics, "delete dashboard", err)
	}
}
