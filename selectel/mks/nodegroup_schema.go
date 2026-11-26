package mks

import (
	"context"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	mksclient "github.com/selectel/mks-go/v2/pkg/mksclient"
	"github.com/terraform-providers/terraform-provider-selectel/selectel/internal/schemadoc"
)

var nodegroupDocs = schemadoc.Resource{Name: "node group", Location: "managed-kubernetes"}

var segmentPattern = regexp.MustCompile(`^[a-z]+-\d+[a-z]$`)

var (
	nodegroupTaintAttrTypes = map[string]attr.Type{
		"key":    types.StringType,
		"value":  types.StringType,
		"effect": types.StringType,
	}
	nodegroupNodeAttrTypes = map[string]attr.Type{
		"id":       types.StringType,
		"ip":       types.StringType,
		"hostname": types.StringType,
	}
	nodegroupCloudConfigAttrTypes = map[string]attr.Type{
		"flavor_id":       types.StringType,
		"cpus":            types.Int64Type,
		"ram_mb":          types.Int64Type,
		"volume_gb":       types.Int64Type,
		"volume_type":     types.StringType,
		"local_volume":    types.BoolType,
		"affinity_policy": types.StringType,
	}
)

func (r *nodegroupResource) Schema(ctx context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	computedString := func() schema.StringAttribute {
		return schema.StringAttribute{
			Computed:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		}
	}
	replacedString := func() schema.StringAttribute {
		return schema.StringAttribute{
			Optional:      true,
			Computed:      true,
			PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), schemadoc.RequiresReplace()},
		}
	}
	replacedBool := func() schema.BoolAttribute {
		return schema.BoolAttribute{
			Optional:      true,
			Computed:      true,
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown(), schemadoc.BoolRequiresReplace()},
		}
	}
	autoscaleLimit := schema.Int64Attribute{Optional: true}
	flavorOrCPUs := schemadoc.ExactlyOneOf("cloud_nodegroup_config.flavor_id", "cloud_nodegroup_config.cpus")

	resp.Schema = schema.Schema{
		MarkdownDescription: "Creates and manages a node group of cloud servers in a Managed Kubernetes cluster using the v2 public API. " +
			"For more information about node groups, see the [official Selectel documentation](https://docs.selectel.ru/en/managed-kubernetes/node-groups/).\n\n" +
			"The API does not return `cpus`, `ram_mb`, and `affinity_policy`, so after an import of a node group created with them " +
			"the plan replaces the node group unless they are in `lifecycle.ignore_changes`.\n\n" +
			"A cluster created inside its maintenance window can enter the `MAINTENANCE` status at once. " +
			"A node group created in the same apply then fails with a conflict error, and a later apply creates it.",
		Attributes: map[string]schema.Attribute{
			"id": nodegroupDocs.String(computedString(), schemadoc.Field{
				Definition: "Unique identifier of the node group in the `<cluster_id>/<nodegroup_id>` format.",
			}),
			"cluster_id": nodegroupDocs.String(schema.StringAttribute{
				Required:      true,
				Validators:    []validator.String{schemadoc.UUID()},
				PlanModifiers: []planmodifier.String{schemadoc.RequiresReplace()},
			}, schemadoc.Field{
				Definition: "Unique identifier of the cluster.",
				Source:     schemadoc.FromResource("selectel_mks_cluster_v2", "cluster ID"),
			}),
			"project_id": nodegroupDocs.String(schema.StringAttribute{
				Optional:   true,
				Computed:   true,
				Validators: []validator.String{schemadoc.LengthAtLeast(1)},
				PlanModifiers: []planmodifier.String{
					sameProjectID,
					stringplanmodifier.UseStateForUnknown(),
					schemadoc.RequiresReplace(),
				},
			}, projectIDField),
			"segment": nodegroupDocs.String(schema.StringAttribute{
				Required: true,
				Validators: []validator.String{
					schemadoc.DescribeString(stringvalidator.RegexMatches(segmentPattern, "must be a pool segment such as ru-7a"),
						"The format is a pool followed by a letter, for example, `ru-7a`."),
				},
				PlanModifiers: []planmodifier.String{schemadoc.RequiresReplace()},
			}, schemadoc.Field{
				Definition: "Pool segment where the nodes of the node group are located. The node group is in the pool of the segment.",
				Concept:    schemadoc.LocationLink("available pool segments", "managed-kubernetes"),
			}),
			"nodes_count": nodegroupDocs.Int64(schema.Int64Attribute{
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
			}, schemadoc.Field{
				Definition: "Number of nodes in the node group. Changing this resizes the node group.",
				Behavior: "Required unless `enable_autoscale` is `true`. If omitted with autoscaling, the node group starts with `autoscale_min_nodes` nodes. " +
					"While autoscaling is enabled, the autoscaler owns the number of nodes, and a changed value is not a change.",
			}),
			"labels": nodegroupDocs.Map(schema.MapAttribute{
				Optional:    true,
				ElementType: types.StringType,
			}, schemadoc.Field{
				Definition: "Kubernetes labels of the nodes in the node group.",
				Behavior:   "Removing the argument removes the labels from the nodes.",
				Concept: schemadoc.Link{
					Topic:  "labels",
					URL:    "https://kubernetes.io/docs/concepts/overview/working-with-objects/labels/",
					Vendor: "Kubernetes",
				},
			}),
			"taints": schema.ListNestedAttribute{
				Optional: true,
				MarkdownDescription: "Kubernetes taints of the nodes in the node group. Removing the argument removes the taints from the nodes. " +
					"Learn more about [Taints](https://docs.selectel.ru/en/managed-kubernetes/node-groups/add-taints/).",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"key": nodegroupDocs.String(schema.StringAttribute{Required: true}, schemadoc.Field{
							Definition: "Key of the taint.",
						}),
						"value": nodegroupDocs.String(schema.StringAttribute{Required: true}, schemadoc.Field{
							Definition: "Value of the taint.",
						}),
						"effect": nodegroupDocs.String(schema.StringAttribute{
							Required: true,
							Validators: []validator.String{schemadoc.OneOf(
								string(mksclient.NoSchedule), string(mksclient.PreferNoSchedule), string(mksclient.NoExecute),
							)},
						}, schemadoc.Field{
							Definition: "Effect of the taint on the pods that do not tolerate it.",
						}),
					},
				},
			},
			"enable_autoscale": nodegroupDocs.Bool(schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  schemadoc.StaticBool(false),
			}, schemadoc.Field{
				Definition: "Enables or disables autoscaling of the node group between `autoscale_min_nodes` and `autoscale_max_nodes`.",
				Behavior:   "If `true`, set `autoscale_min_nodes` and `autoscale_max_nodes`.",
				Concept: schemadoc.Link{
					Title: "Autoscaling",
					URL:   "https://docs.selectel.ru/en/managed-kubernetes/node-groups/cluster-autoscaler/",
				},
			}),
			"autoscale_min_nodes": nodegroupDocs.Int64(autoscaleLimit, schemadoc.Field{
				Definition: "Minimum number of nodes while autoscaling is enabled.",
			}),
			"autoscale_max_nodes": nodegroupDocs.Int64(autoscaleLimit, schemadoc.Field{
				Definition: "Maximum number of nodes while autoscaling is enabled.",
			}),
			"user_data": nodegroupDocs.String(replacedString(), schemadoc.Field{
				Definition: "Script in Base64 that the nodes run on their first boot.",
				Concept: schemadoc.Link{
					Title: "User data",
					URL:   "https://docs.selectel.ru/en/managed-kubernetes/node-groups/user-data/",
				},
			}),
			"install_nvidia_device_plugin": nodegroupDocs.Bool(replacedBool(), schemadoc.Field{
				Definition: "Enables or disables the installation of the NVIDIA Device Plugin and GPU drivers on the nodes.",
				Behavior: "If omitted, the API enables it for a GPU flavor. " +
					"If `true`, set `cloud_nodegroup_config.flavor_id` to a GPU flavor instead of `cpus` and `ram_mb`.",
			}),
			"preemptible": nodegroupDocs.Bool(schema.BoolAttribute{
				Optional:      true,
				Computed:      true,
				Default:       schemadoc.StaticBool(false),
				PlanModifiers: []planmodifier.Bool{schemadoc.BoolRequiresReplace()},
			}, schemadoc.Field{
				Definition: "Whether the nodes are preemptible.",
				Concept: schemadoc.Link{
					Title: "Preemptible node groups",
					URL:   "https://docs.selectel.ru/en/managed-kubernetes/node-groups/preemptible-node-groups/",
				},
			}),
			"cloud_nodegroup_config": schema.SingleNestedAttribute{
				Required:            true,
				MarkdownDescription: "Configuration of the cloud servers of the nodes. Changing any of its arguments creates a new node group.",
				Attributes: map[string]schema.Attribute{
					"flavor_id": nodegroupDocs.String(schema.StringAttribute{
						Optional:      true,
						Computed:      true,
						Validators:    []validator.String{flavorOrCPUs},
						PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown(), schemadoc.RequiresReplace()},
					}, schemadoc.Field{
						Definition: "Unique identifier of the flavor of the nodes.",
						Behavior:   "If omitted, the API creates a flavor from `cpus` and `ram_mb`.",
						Source:     schemadoc.FromOpenStackCLI("flavor ID", "openstack flavor list", "ID"),
						Concept: schemadoc.Link{
							Title: "Node group configurations",
							URL:   "https://docs.selectel.ru/en/managed-kubernetes/node-groups/configurations/",
						},
					}),
					"cpus": nodegroupDocs.Int64(schema.Int64Attribute{
						Optional:      true,
						Validators:    []validator.Int64{flavorOrCPUs, schemadoc.AlsoRequires("cloud_nodegroup_config.ram_mb")},
						PlanModifiers: []planmodifier.Int64{schemadoc.Int64RequiresReplace()},
					}, schemadoc.Field{
						Definition: "Number of vCPUs of each node.",
					}),
					"ram_mb": nodegroupDocs.Int64(schema.Int64Attribute{
						Optional:      true,
						Validators:    []validator.Int64{schemadoc.AlsoRequires("cloud_nodegroup_config.cpus")},
						PlanModifiers: []planmodifier.Int64{schemadoc.Int64RequiresReplace()},
					}, schemadoc.Field{
						Definition: "Amount of RAM of each node in MB.",
					}),
					"volume_gb": nodegroupDocs.Int64(schema.Int64Attribute{
						Optional:      true,
						Computed:      true,
						PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown(), schemadoc.Int64RequiresReplace()},
					}, schemadoc.Field{
						Definition: "Size of the volume of each node in GB.",
						Behavior: "A flavor with a local disk defines the size, so omit this argument with such a flavor: " +
							"a different value fails the apply after the node group is created.",
					}),
					"volume_type": nodegroupDocs.String(replacedString(), schemadoc.Field{
						Definition: "Type of the network volume of each node in the `<type>.<segment>` format, for example, `fast.ru-7a`.",
						Behavior:   "If `local_volume` is `true`, omit this argument.",
						Concept: schemadoc.Link{
							Title: "Network volume types",
							URL:   "https://docs.selectel.ru/en/cloud-servers/volumes/about-network-volumes/#network-volume-types",
						},
					}),
					"local_volume": nodegroupDocs.Bool(replacedBool(), schemadoc.Field{
						Definition: "Whether the nodes use a local volume instead of a network one.",
						Behavior: "A flavor with a local disk sets it, so omit this argument with such a flavor: " +
							"a different value fails the apply after the node group is created.",
					}),
					"affinity_policy": nodegroupDocs.String(schema.StringAttribute{
						Optional:      true,
						PlanModifiers: []planmodifier.String{schemadoc.RequiresReplace()},
					}, schemadoc.Field{
						Definition: "Affinity policy of the nodes, for example, `soft-anti-affinity`.",
						Behavior:   "If omitted, the API uses its default policy.",
					}),
				},
			},
			"nodegroup_type": nodegroupDocs.String(computedString(), schemadoc.Field{
				Definition: "Type of the node group: `STANDARD` or `GPU`.",
			}),
			"status": nodegroupDocs.String(computedString(), schemadoc.Field{
				Definition: "Node group status.",
			}),
			"nodes": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "Nodes of the node group.",
				PlanModifiers:       []planmodifier.List{listplanmodifier.UseStateForUnknown()},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id": nodegroupDocs.String(schema.StringAttribute{Computed: true}, schemadoc.Field{
							Definition: "Unique identifier of the node.",
						}),
						"ip": nodegroupDocs.String(schema.StringAttribute{Computed: true}, schemadoc.Field{
							Definition: "IP address of the node.",
						}),
						"hostname": nodegroupDocs.String(schema.StringAttribute{Computed: true}, schemadoc.Field{
							Definition: "Hostname of the node.",
						}),
					},
				},
			},
		},
		Blocks: map[string]schema.Block{
			"timeouts": nodegroupDocs.Timeouts(ctx, nodegroupTimeouts),
		},
	}
}
