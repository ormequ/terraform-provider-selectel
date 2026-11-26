package mks

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nodegroupConfig() nodegroupModel {
	m := nodegroupPlan()
	m.ProjectID = types.StringNull()
	m.UserData = types.StringNull()
	m.InstallNvidiaDevicePlugin = types.BoolNull()
	m.NodegroupType = types.StringNull()
	m.Status = types.StringNull()
	m.Nodes = types.ListNull(types.ObjectType{AttrTypes: nodegroupNodeAttrTypes})
	m.ID = types.StringNull()

	return m
}

func TestNodegroupValidateConfigRules(t *testing.T) {
	r := &nodegroupResource{}
	for _, tt := range []struct {
		name   string
		mutate func(m *nodegroupModel)
		want   string
	}{
		{name: "nodes_count is required without autoscaling", mutate: func(m *nodegroupModel) {
			m.NodesCount = types.Int64Null()
		}, want: "Missing node count"},
		{name: "autoscaling needs no nodes_count", mutate: func(m *nodegroupModel) {
			m.NodesCount, m.EnableAutoscale = types.Int64Null(), types.BoolValue(true)
			m.AutoscaleMinNodes, m.AutoscaleMaxNodes = types.Int64Value(1), types.Int64Value(3)
		}},
		{name: "autoscaling needs both limits", mutate: func(m *nodegroupModel) {
			m.EnableAutoscale, m.AutoscaleMinNodes = types.BoolValue(true), types.Int64Value(1)
		}, want: "Missing autoscaling limits"},
		{name: "the NVIDIA device plugin needs a flavor", mutate: func(m *nodegroupModel) {
			m.InstallNvidiaDevicePlugin = types.BoolValue(true)
			m.CloudNodegroupConfig = cloudConfigObject(map[string]attr.Value{
				"flavor_id": types.StringNull(), "cpus": types.Int64Value(2), "ram_mb": types.Int64Value(4096),
			})
		}, want: "NVIDIA device plugin needs a GPU flavor"},
		{name: "a disabled NVIDIA device plugin takes cpus", mutate: func(m *nodegroupModel) {
			m.InstallNvidiaDevicePlugin = types.BoolValue(false)
			m.CloudNodegroupConfig = cloudConfigObject(map[string]attr.Value{
				"flavor_id": types.StringNull(), "cpus": types.Int64Value(2), "ram_mb": types.Int64Value(4096),
			})
		}},
		{name: "a local volume rejects volume_type", mutate: func(m *nodegroupModel) {
			m.CloudNodegroupConfig = cloudConfigObject(map[string]attr.Value{"local_volume": types.BoolValue(true)})
		}, want: "Volume type with a local volume"},
		{name: "a network volume takes volume_type", mutate: func(_ *nodegroupModel) {}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			config := nodegroupConfig()
			tt.mutate(&config)
			resp := resource.ValidateConfigResponse{}
			r.ValidateConfig(context.Background(), resource.ValidateConfigRequest{
				Config: tfsdk.Config(nodegroupStateOf(t, r, config)),
			}, &resp)
			if tt.want == "" {
				assert.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)

				return
			}
			require.True(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			assert.Equal(t, tt.want, resp.Diagnostics.Errors()[0].Summary())
		})
	}
}

func TestNodegroupSegmentValidator(t *testing.T) {
	var s resource.SchemaResponse
	(&nodegroupResource{}).Schema(context.Background(), resource.SchemaRequest{}, &s)
	for _, tt := range []struct {
		value string
		valid bool
	}{
		{value: "ru-7a", valid: true},
		{value: "gis-1b", valid: true},
		{value: "ru-7"},
		{value: "ru-7ab"},
		{value: "RU-7a"},
		{value: "SPB-3"},
	} {
		t.Run(tt.value, func(t *testing.T) {
			var resp validator.StringResponse
			for _, rule := range s.Schema.Attributes["segment"].(schema.StringAttribute).Validators {
				rule.ValidateString(context.Background(), validator.StringRequest{
					Path: path.Root("segment"), ConfigValue: types.StringValue(tt.value),
				}, &resp)
			}
			assert.Equal(t, !tt.valid, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
		})
	}
}

func TestNodegroupModifyPlan(t *testing.T) {
	r := &nodegroupResource{}
	for _, tt := range []struct {
		name   string
		state  func(m *nodegroupModel)
		mutate func(m *nodegroupModel)
		check  func(t *testing.T, plan nodegroupModel)
	}{
		{
			name: "an unchanged node group keeps the status and the nodes", mutate: func(_ *nodegroupModel) {},
			check: func(t *testing.T, plan nodegroupModel) {
				assert.Equal(t, types.StringValue("ACTIVE"), plan.Status)
				assert.Equal(t, nodesList("nodegroup-1", 2), plan.Nodes)
			},
		},
		{name: "a change sets the status and the nodes unknown", mutate: func(m *nodegroupModel) {
			m.Labels = labelsMap(map[string]string{"role": "db"})
		}, check: func(t *testing.T, plan nodegroupModel) {
			assert.True(t, plan.Status.IsUnknown())
			assert.True(t, plan.Nodes.IsUnknown())
		}},
		{name: "a count change without autoscaling stays", mutate: func(m *nodegroupModel) {
			m.NodesCount = types.Int64Value(3)
		}, check: func(t *testing.T, plan nodegroupModel) {
			assert.Equal(t, types.Int64Value(3), plan.NodesCount)
		}},
		{name: "a count change under autoscaling is no change", state: func(m *nodegroupModel) {
			m.EnableAutoscale, m.AutoscaleMinNodes, m.AutoscaleMaxNodes = types.BoolValue(true), types.Int64Value(1), types.Int64Value(4)
		}, mutate: func(m *nodegroupModel) {
			m.NodesCount = types.Int64Value(3)
		}, check: func(t *testing.T, plan nodegroupModel) {
			assert.Equal(t, types.Int64Value(2), plan.NodesCount)
			assert.Equal(t, types.StringValue("ACTIVE"), plan.Status)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			state := nodegroupState()
			if tt.state != nil {
				tt.state(&state)
			}
			plan := state
			tt.mutate(&plan)
			req := resource.ModifyPlanRequest{
				Config: tfsdk.Config(nodegroupStateOf(t, r, plan)), Plan: tfsdk.Plan(nodegroupStateOf(t, r, plan)),
				State: nodegroupStateOf(t, r, state),
			}
			resp := resource.ModifyPlanResponse{Plan: req.Plan}
			r.ModifyPlan(context.Background(), req, &resp)
			require.False(t, resp.Diagnostics.HasError(), "%v", resp.Diagnostics)
			var got nodegroupModel
			require.False(t, resp.Plan.Get(context.Background(), &got).HasError())
			tt.check(t, got)
		})
	}
}

func TestNodegroupFlavorRules(t *testing.T) {
	r := &nodegroupResource{}
	var s resource.SchemaResponse
	r.Schema(context.Background(), resource.SchemaRequest{}, &s)
	cloud := s.Schema.Attributes["cloud_nodegroup_config"].(schema.SingleNestedAttribute).Attributes
	for _, tt := range []struct {
		name   string
		config map[string]attr.Value
		want   string
	}{
		{name: "a flavor alone is accepted", config: map[string]attr.Value{}},
		{name: "cpus and ram_mb alone are accepted", config: map[string]attr.Value{
			"flavor_id": types.StringNull(), "cpus": types.Int64Value(2), "ram_mb": types.Int64Value(4096),
		}},
		{name: "a flavor with cpus is rejected", config: map[string]attr.Value{
			"cpus": types.Int64Value(2), "ram_mb": types.Int64Value(4096),
		}, want: "cloud_nodegroup_config.flavor_id"},
		{name: "neither a flavor nor cpus is rejected", config: map[string]attr.Value{
			"flavor_id": types.StringNull(),
		}, want: "cloud_nodegroup_config.cpus"},
		{name: "cpus without ram_mb is rejected", config: map[string]attr.Value{
			"flavor_id": types.StringNull(), "cpus": types.Int64Value(2),
		}, want: "cloud_nodegroup_config.ram_mb"},
		{name: "ram_mb without cpus is rejected", config: map[string]attr.Value{
			"ram_mb": types.Int64Value(4096),
		}, want: "cloud_nodegroup_config.cpus"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			model := nodegroupConfig()
			model.CloudNodegroupConfig = cloudConfigObject(tt.config)
			config := tfsdk.Config(nodegroupStateOf(t, r, model))
			var cfg nodegroupCloudConfigModel
			require.False(t, model.CloudNodegroupConfig.As(context.Background(), &cfg, basetypes.ObjectAsOptions{}).HasError())

			var diags diag.Diagnostics
			at := func(name string) (path.Path, path.Expression) {
				return path.Root("cloud_nodegroup_config").AtName(name), path.MatchRoot("cloud_nodegroup_config").AtName(name)
			}
			for _, rule := range cloud["flavor_id"].(schema.StringAttribute).Validators {
				p, e := at("flavor_id")
				var resp validator.StringResponse
				rule.ValidateString(context.Background(), validator.StringRequest{Path: p, PathExpression: e, ConfigValue: cfg.FlavorID, Config: config}, &resp)
				diags.Append(resp.Diagnostics...)
			}
			for name, value := range map[string]types.Int64{"cpus": cfg.CPUs, "ram_mb": cfg.RAMMB} {
				for _, rule := range cloud[name].(schema.Int64Attribute).Validators {
					p, e := at(name)
					var resp validator.Int64Response
					rule.ValidateInt64(context.Background(), validator.Int64Request{Path: p, PathExpression: e, ConfigValue: value, Config: config}, &resp)
					diags.Append(resp.Diagnostics...)
				}
			}
			if tt.want == "" {
				assert.False(t, diags.HasError(), "%v", diags)

				return
			}
			assertErrorContains(t, diags, tt.want)
		})
	}
}
