package provider

import (
	"context"
	"net/http"
	"net/url"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// locationNameRe is the form of an uptime location name.
var locationNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,47}$`)

type uptimeLocationResource struct{ base }

func newUptimeLocationResource() resource.Resource { return &uptimeLocationResource{} }

var _ resource.ResourceWithImportState = (*uptimeLocationResource)(nil)

type locationModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	Key       types.String `tfsdk:"key"`
	KeyPrefix types.String `tfsdk:"key_prefix"`
}

type apiLocation struct {
	Name      string `json:"name"`
	KeyPrefix string `json:"key_prefix"`
	Builtin   bool   `json:"builtin"`
}

func (r *uptimeLocationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_uptime_location"
}

func (r *uptimeLocationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		Description: "An uptime location: a place checks run from, served by a host agent started with VISIONARY_AGENT_LOCATION and the location's key. " +
			"Visionary shows the key once, when the location is made, so it is kept in the state as secret. Needs an admin key.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Description: "The location's name.", Computed: true, PlanModifiers: keep},
			"name": schema.StringAttribute{
				Description: "Name checks use in their locations, e.g. frankfurt-vps: 1–48 lowercase letters, digits, dots, dashes or underscores.",
				Required:    true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{stringvalidator.RegexMatches(locationNameRe, "must be 1–48 lowercase letters, digits, dots, dashes or underscores"),
					stringvalidator.NoneOf("server")},
			},
			"key":        schema.StringAttribute{Description: "The location key (VISIONARY_KEY for its runner). Empty for an imported location.", Computed: true, Sensitive: true, PlanModifiers: keep},
			"key_prefix": schema.StringAttribute{Description: "The visible start of the key.", Computed: true, PlanModifiers: keep},
		},
	}
}

func (r *uptimeLocationResource) find(ctx context.Context, name string) (*apiLocation, error) {
	var out struct {
		Locations []apiLocation `json:"locations"`
	}
	if err := r.c.do(ctx, http.MethodGet, "/api/uptime/locations", nil, &out); err != nil {
		return nil, err
	}
	for _, l := range out.Locations {
		if l.Name == name && !l.Builtin {
			return &l, nil
		}
	}
	return nil, nil
}

func (r *uptimeLocationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan locationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var out struct {
		Location apiLocation `json:"location"`
		Secret   string      `json:"secret"`
	}
	if err := r.c.do(ctx, http.MethodPost, "/api/uptime/locations", map[string]any{"name": plan.Name.ValueString()}, &out); err != nil {
		apiDiag(&resp.Diagnostics, "create uptime location", err)
		return
	}
	plan.ID, plan.Name = types.StringValue(out.Location.Name), types.StringValue(out.Location.Name)
	plan.Key, plan.KeyPrefix = types.StringValue(out.Secret), types.StringValue(out.Location.KeyPrefix)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *uptimeLocationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state locationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	l, err := r.find(ctx, state.ID.ValueString())
	if err != nil {
		apiDiag(&resp.Diagnostics, "read uptime location", err)
		return
	}
	if l == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	state.Name, state.KeyPrefix = types.StringValue(l.Name), types.StringValue(l.KeyPrefix)
	if state.Key.IsNull() || state.Key.IsUnknown() {
		state.Key = types.StringValue("")
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// Update is never called with a change: a new name makes a new location.
func (r *uptimeLocationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan locationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *uptimeLocationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state locationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	err := r.c.do(ctx, http.MethodDelete, "/api/uptime/locations/"+url.PathEscape(state.ID.ValueString()), nil, nil)
	if err != nil && !isNotFound(err) {
		apiDiag(&resp.Diagnostics, "delete uptime location", err)
	}
}

func (r *uptimeLocationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), req.ID)...)
}
