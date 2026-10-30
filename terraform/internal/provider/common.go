package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// base is what every resource shares: the API client and import by id.
type base struct {
	c *client
}

func (b *base) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (b *base) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if _, err := strconv.ParseInt(req.ID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import id", "Import by the numeric id Visionary shows, such as 12.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func idAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Description:   "The id Visionary gave it.",
		Computed:      true,
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func idString(id int64) types.String { return types.StringValue(strconv.FormatInt(id, 10)) }

func parseID(v types.String) (int64, error) {
	return strconv.ParseInt(v.ValueString(), 10, 64)
}

// The helpers below turn what the server answers into state. Optional
// attributes that were left out stay null when the server holds the empty
// value, so a plan stays clean; ref is the plan after a change, or the prior
// state on a refresh.

func optString(v string, ref types.String) types.String {
	if v == "" && ref.IsNull() {
		return types.StringNull()
	}
	return types.StringValue(v)
}

func optInt(v int64, ref types.Int64) types.Int64 {
	if v == 0 && ref.IsNull() {
		return types.Int64Null()
	}
	return types.Int64Value(v)
}

func optFloat(v float64, ref types.Float64) types.Float64 {
	if v == 0 && ref.IsNull() {
		return types.Float64Null()
	}
	return types.Float64Value(v)
}

func optStringList(v []string, ref types.List) types.List {
	if len(v) == 0 && ref.IsNull() {
		return types.ListNull(types.StringType)
	}
	elems := make([]attr.Value, len(v))
	for i, s := range v {
		elems[i] = types.StringValue(s)
	}
	return types.ListValueMust(types.StringType, elems)
}

func optIDList(v []int64, ref types.List) types.List {
	out := make([]string, len(v))
	for i, id := range v {
		out[i] = strconv.FormatInt(id, 10)
	}
	return optStringList(out, ref)
}

func optStringMap(v map[string]string, ref types.Map) types.Map {
	if len(v) == 0 && ref.IsNull() {
		return types.MapNull(types.StringType)
	}
	elems := make(map[string]attr.Value, len(v))
	for k, s := range v {
		elems[k] = types.StringValue(s)
	}
	return types.MapValueMust(types.StringType, elems)
}

func stringsOf(ctx context.Context, l types.List, diags *diag.Diagnostics) []string {
	if l.IsNull() || l.IsUnknown() {
		return nil
	}
	var out []string
	diags.Append(l.ElementsAs(ctx, &out, false)...)
	return out
}

func idsOf(ctx context.Context, l types.List, diags *diag.Diagnostics) []int64 {
	var out []int64
	for _, s := range stringsOf(ctx, l, diags) {
		id, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			diags.AddError("Invalid id", fmt.Sprintf("%q is not a Visionary id", s))
			continue
		}
		out = append(out, id)
	}
	return out
}

func mapOf(ctx context.Context, m types.Map, diags *diag.Diagnostics) map[string]string {
	if m.IsNull() || m.IsUnknown() {
		return nil
	}
	out := map[string]string{}
	diags.Append(m.ElementsAs(ctx, &out, false)...)
	return out
}

// apiDiag reports a failed call.
func apiDiag(diags *diag.Diagnostics, what string, err error) {
	diags.AddError("Visionary: "+what, err.Error())
}
