package selectel

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/identityschema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/defaults"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
)

func (r resourceDocs) idResourceAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Computed:      true,
		Description:   r.idDescription() + ".",
		PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func (r resourceDocs) idIdentityAttribute(controlPanelHint string) identityschema.StringAttribute {
	return identityschema.StringAttribute{
		RequiredForImport: true,
		Description:       fmt.Sprintf("%s, for example, `%s`. %s", r.idDescription(), exampleResourceID, controlPanelHint),
	}
}

func (r resourceDocs) regionResourceAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Required:      true,
		Description:   r.regionDescription() + " " + regionLearnMore,
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
}

func (r resourceDocs) regionIdentityAttribute(controlPanelHint string) identityschema.StringAttribute {
	return identityschema.StringAttribute{
		RequiredForImport: true,
		Description:       r.regionDescription() + " " + controlPanelHint + " " + regionLearnMore,
	}
}

func projectIDResourceAttribute() schema.StringAttribute {
	return schema.StringAttribute{
		Required:      true,
		Description:   projectIDDescription + " " + projectIDFromResource + " " + projectIDLearnMore,
		PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
	}
}

func projectIDIdentityAttribute() identityschema.StringAttribute {
	return identityschema.StringAttribute{
		RequiredForImport: true,
		Description:       projectIDDescription + " " + projectIDFromControlPanel + " " + projectIDLearnMore,
	}
}

// withFrameworkDocsHints is the framework version of withDocsHints.
func (r resourceDocs) withFrameworkDocsHints(attrs map[string]schema.Attribute) map[string]schema.Attribute {
	ctx := context.Background()
	replaceHint := fmt.Sprintf(" Changing this creates a new %s.", r.Name)

	for name, attr := range attrs {
		switch a := attr.(type) {
		case schema.StringAttribute:
			if anyRequiresReplace(ctx, a.PlanModifiers) {
				a.Description += replaceHint
			}
			if a.Default != nil {
				resp := &defaults.StringResponse{}
				a.Default.DefaultString(ctx, defaults.StringRequest{}, resp)
				a.Description += defaultHint(resp.PlanValue.ValueString())
			}
			attrs[name] = a
		case schema.BoolAttribute:
			if anyRequiresReplace(ctx, a.PlanModifiers) {
				a.Description += replaceHint
			}
			if a.Default != nil {
				resp := &defaults.BoolResponse{}
				a.Default.DefaultBool(ctx, defaults.BoolRequest{}, resp)
				a.Description += defaultHint(resp.PlanValue.ValueBool())
			}
			attrs[name] = a
		case schema.Int64Attribute:
			if anyRequiresReplace(ctx, a.PlanModifiers) {
				a.Description += replaceHint
			}
			if a.Default != nil {
				resp := &defaults.Int64Response{}
				a.Default.DefaultInt64(ctx, defaults.Int64Request{}, resp)
				a.Description += defaultHint(resp.PlanValue.ValueInt64())
			}
			attrs[name] = a
		case schema.SingleNestedAttribute:
			if anyRequiresReplace(ctx, a.PlanModifiers) {
				a.Description += replaceHint
			}
			r.withFrameworkDocsHints(a.Attributes)
			attrs[name] = a
		}
	}

	return attrs
}

type describer interface {
	Description(ctx context.Context) string
}

func anyRequiresReplace[M describer](ctx context.Context, modifiers []M) bool {
	for _, m := range modifiers {
		// The framework has no ForceNew flag; RequiresReplace is recognised by
		// its stock description, so a custom RequiresReplaceIf text is missed.
		if strings.Contains(m.Description(ctx), "Terraform will destroy and recreate the resource") {
			return true
		}
	}

	return false
}
