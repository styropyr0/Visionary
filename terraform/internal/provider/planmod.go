package provider

import (
	"context"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
)

// Plans a server-filled default as unchanged while the attributes it depends on
// stay the same, so an unrelated change doesn't show it as "known after apply"
type keepIfSame struct{ on []string }

func keepIf(on ...string) keepIfSame { return keepIfSame{on: on} }

func (k keepIfSame) Description(context.Context) string {
	return "keeps the stored value while " + strings.Join(k.on, " and ") + " stay the same"
}

func (k keepIfSame) MarkdownDescription(ctx context.Context) string { return k.Description(ctx) }

func (k keepIfSame) same(ctx context.Context, plan tfsdk.Plan, state tfsdk.State, diags *diag.Diagnostics) bool {
	for _, name := range k.on {
		var p, s attr.Value
		diags.Append(plan.GetAttribute(ctx, path.Root(name), &p)...)
		diags.Append(state.GetAttribute(ctx, path.Root(name), &s)...)
		if p == nil || s == nil || p.IsUnknown() || !p.Equal(s) {
			return false
		}
	}
	return true
}

func (k keepIfSame) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.ConfigValue.IsNull() && !req.StateValue.IsNull() && resp.PlanValue.IsUnknown() && k.same(ctx, req.Plan, req.State, &resp.Diagnostics) {
		resp.PlanValue = req.StateValue
	}
}

func (k keepIfSame) PlanModifyBool(ctx context.Context, req planmodifier.BoolRequest, resp *planmodifier.BoolResponse) {
	if req.ConfigValue.IsNull() && !req.StateValue.IsNull() && resp.PlanValue.IsUnknown() && k.same(ctx, req.Plan, req.State, &resp.Diagnostics) {
		resp.PlanValue = req.StateValue
	}
}

func (k keepIfSame) PlanModifyInt64(ctx context.Context, req planmodifier.Int64Request, resp *planmodifier.Int64Response) {
	if req.ConfigValue.IsNull() && !req.StateValue.IsNull() && resp.PlanValue.IsUnknown() && k.same(ctx, req.Plan, req.State, &resp.Diagnostics) {
		resp.PlanValue = req.StateValue
	}
}

func (k keepIfSame) PlanModifyFloat64(ctx context.Context, req planmodifier.Float64Request, resp *planmodifier.Float64Response) {
	if req.ConfigValue.IsNull() && !req.StateValue.IsNull() && resp.PlanValue.IsUnknown() && k.same(ctx, req.Plan, req.State, &resp.Diagnostics) {
		resp.PlanValue = req.StateValue
	}
}
