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

	for name, attr := range attrs {
		switch a := attr.(type) {
		case schema.BoolAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.DynamicAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.Float32Attribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.Float64Attribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.Int32Attribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.Int64Attribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.ListAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.MapAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.NumberAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.ObjectAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.SetAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.StringAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			attrs[name] = a
		case schema.ListNestedAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			r.withFrameworkDocsHints(a.NestedObject.Attributes)
			attrs[name] = a
		case schema.MapNestedAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			r.withFrameworkDocsHints(a.NestedObject.Attributes)
			attrs[name] = a
		case schema.SetNestedAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			r.withFrameworkDocsHints(a.NestedObject.Attributes)
			attrs[name] = a
		case schema.SingleNestedAttribute:
			a.Description += replaceHint(ctx, r.Name, a.PlanModifiers) + frameworkDefaultHint(ctx, a.Default)
			r.withFrameworkDocsHints(a.Attributes)
			attrs[name] = a
		}
	}

	return attrs
}

// withFrameworkBlockDocsHints covers nested blocks, which SDKv2 declares as
// attributes with an Elem resource.
func (r resourceDocs) withFrameworkBlockDocsHints(blocks map[string]schema.Block) map[string]schema.Block {
	ctx := context.Background()

	for name, block := range blocks {
		switch b := block.(type) {
		case schema.ListNestedBlock:
			b.Description += replaceHint(ctx, r.Name, b.PlanModifiers)
			r.withFrameworkDocsHints(b.NestedObject.Attributes)
			r.withFrameworkBlockDocsHints(b.NestedObject.Blocks)
			blocks[name] = b
		case schema.SetNestedBlock:
			b.Description += replaceHint(ctx, r.Name, b.PlanModifiers)
			r.withFrameworkDocsHints(b.NestedObject.Attributes)
			r.withFrameworkBlockDocsHints(b.NestedObject.Blocks)
			blocks[name] = b
		case schema.SingleNestedBlock:
			b.Description += replaceHint(ctx, r.Name, b.PlanModifiers)
			r.withFrameworkDocsHints(b.Attributes)
			r.withFrameworkBlockDocsHints(b.Blocks)
			blocks[name] = b
		}
	}

	return blocks
}

// frameworkDefaultHint renders scalar defaults only: SDKv2 rejects defaults on
// lists and sets, and no map in this provider has one.
func frameworkDefaultHint(ctx context.Context, d any) string {
	switch d := d.(type) {
	case defaults.Bool:
		resp := &defaults.BoolResponse{}
		d.DefaultBool(ctx, defaults.BoolRequest{}, resp)

		return defaultHint(resp.PlanValue.ValueBool())
	case defaults.Float32:
		resp := &defaults.Float32Response{}
		d.DefaultFloat32(ctx, defaults.Float32Request{}, resp)

		return defaultHint(resp.PlanValue.ValueFloat32())
	case defaults.Float64:
		resp := &defaults.Float64Response{}
		d.DefaultFloat64(ctx, defaults.Float64Request{}, resp)

		return defaultHint(resp.PlanValue.ValueFloat64())
	case defaults.Int32:
		resp := &defaults.Int32Response{}
		d.DefaultInt32(ctx, defaults.Int32Request{}, resp)

		return defaultHint(resp.PlanValue.ValueInt32())
	case defaults.Int64:
		resp := &defaults.Int64Response{}
		d.DefaultInt64(ctx, defaults.Int64Request{}, resp)

		return defaultHint(resp.PlanValue.ValueInt64())
	case defaults.Number:
		resp := &defaults.NumberResponse{}
		d.DefaultNumber(ctx, defaults.NumberRequest{}, resp)

		return defaultHint(resp.PlanValue.ValueBigFloat())
	case defaults.String:
		resp := &defaults.StringResponse{}
		d.DefaultString(ctx, defaults.StringRequest{}, resp)

		return defaultHint(resp.PlanValue.ValueString())
	}

	return ""
}

type describer interface {
	Description(ctx context.Context) string
}

func replaceHint[M describer](ctx context.Context, name string, modifiers []M) string {
	for _, m := range modifiers {
		// The framework has no ForceNew flag; RequiresReplace is recognised by
		// its stock description, so a custom RequiresReplaceIf text is missed.
		if strings.Contains(m.Description(ctx), "Terraform will destroy and recreate the resource") {
			return fmt.Sprintf(" Changing this creates a new %s.", name)
		}
	}

	return ""
}
