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

func TestFrameworkResourceDocsIDAttributes(t *testing.T) {
	docs := resourceDocs{Name: "public port"}

	res := docs.idResourceAttribute()
	assert.True(t, res.Computed)
	assert.Equal(t, "Unique identifier of the public port.", res.Description)

	identity := docs.idIdentityAttribute("Copy it from the card.")
	assert.True(t, identity.RequiredForImport)
	assert.Equal(t,
		"Unique identifier of the public port, for example, `b311ce58-2658-46b5-b733-7a0f418703f2`. Copy it from the card.",
		identity.Description)
}

func TestFrameworkHelpersMatchSDKv2(t *testing.T) {
	docs := resourceDocs{Name: "public port"}
	hint := "Copy it from the card."

	assert.Equal(t, docs.regionResourceSchema().Description, docs.regionResourceAttribute().Description)
	assert.Equal(t, docs.regionIdentitySchema(hint).Description, docs.regionIdentityAttribute(hint).Description)
	assert.Equal(t, projectIDResourceSchema().Description, projectIDResourceAttribute().Description)
	assert.Equal(t, projectIDIdentitySchema().Description, projectIDIdentityAttribute().Description)
}
