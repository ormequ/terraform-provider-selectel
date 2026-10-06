package selectel

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/stretchr/testify/assert"
)

func TestWithFrameworkDocsHints(t *testing.T) {
	s := resourceDocs{Name: "public port"}.withFrameworkDocsHints(map[string]schema.Attribute{
		"force_new": schema.StringAttribute{
			Required:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			Description:   "Force new field.",
		},
		"with_default": schema.BoolAttribute{
			Optional:    true,
			Computed:    true,
			Default:     booldefault.StaticBool(true),
			Description: "Bool field.",
		},
		"empty_default": schema.StringAttribute{
			Optional:    true,
			Computed:    true,
			Default:     stringdefault.StaticString(""),
			Description: "String field.",
		},
		"plain": schema.StringAttribute{
			Computed:    true,
			Description: "Computed field.",
		},
		"nested": schema.SingleNestedAttribute{
			Optional: true,
			Attributes: map[string]schema.Attribute{
				"inner": schema.StringAttribute{
					Optional:      true,
					PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplaceIfConfigured()},
					Description:   "Inner field.",
				},
			},
		},
	})

	assert.Equal(t, "Force new field. Changing this creates a new public port.", s["force_new"].GetDescription())
	assert.Equal(t, "Bool field. The default value is `true`.", s["with_default"].GetDescription())
	assert.Equal(t, "String field. The default value is an empty string.", s["empty_default"].GetDescription())
	assert.Equal(t, "Computed field.", s["plain"].GetDescription())
	assert.Equal(t, "Inner field. Changing this creates a new public port.",
		s["nested"].(schema.SingleNestedAttribute).Attributes["inner"].GetDescription())
}

// The framework helpers must render the same descriptions as their SDKv2
// counterparts, so _v1 and _v2 resources read alike in the docs.
func TestFrameworkHelpersMatchSDKv2(t *testing.T) {
	docs := resourceDocs{Name: "public port"}
	hint := "Copy it from the card."

	assert.Equal(t, docs.idResourceSchema().Description, docs.idResourceAttribute().Description)
	assert.Equal(t, docs.idIdentitySchema(hint).Description, docs.idIdentityAttribute(hint).Description)
	assert.Equal(t, docs.regionResourceSchema().Description, docs.regionResourceAttribute().Description)
	assert.Equal(t, docs.regionIdentitySchema(hint).Description, docs.regionIdentityAttribute(hint).Description)
	assert.Equal(t, projectIDResourceSchema().Description, projectIDResourceAttribute().Description)
	assert.Equal(t, projectIDIdentitySchema().Description, projectIDIdentityAttribute().Description)
}
