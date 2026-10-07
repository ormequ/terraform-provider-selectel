---
page_title: "Selectel: selectel_mks_nodegroup_v2"
description: |-
  Creates and manages a cloud node group in Selectel Managed Kubernetes using API v2.
---

# selectel\_mks\_nodegroup\_v2

Creates and manages a Managed Kubernetes cloud node group using API v2. For more information about node groups, see the [official Selectel documentation](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/).

The node group takes the project from the provider configuration: set `project_id` in the provider or the `INFRA_PROJECT_ID` environment variable. The pool is derived from `segment`.

The resource waits for the node group tasks of every operation. When a task fails, the operation fails with the task type, ID and error details.

## Example Usage

```terraform
resource "selectel_mks_nodegroup_v2" "nodegroup_1" {
  cluster_id  = selectel_mks_cluster_v2.cluster_1.id
  segment     = "ru-7a"
  nodes_count = 3

  cloud_nodegroup_config = {
    cpus        = 2
    ram_mb      = 4096
    volume_gb   = 32
    volume_type = "fast.ru-7a"
  }

  labels = {
    "label-key0" = "label-value0"
  }
  taints = [
    {
      key    = "test-key-0"
      value  = "test-value-0"
      effect = "NoSchedule"
    },
  ]
}
```

## Argument Reference

* `cluster_id` - (Required) Unique identifier of the cluster. Changing this creates a new node group. Retrieved from the [selectel_mks_cluster_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/mks_cluster_v2) resource. The cluster must have `workers_type = "CLOUD"`: a cloud node group for a `DEDICATED` cluster fails at plan, or before any request when the cluster is created in the same apply.

* `segment` - (Required) Pool segment where all nodes of the node group are located, for example, `ru-7a`. Changing this creates a new node group. Learn more about available pool segments in the [Availability matrix](https://docs.selectel.ru/en/control-panel-actions/availability-matrix/#managed-kubernetes).

* `nodes_count` - (Optional) Number of worker nodes in the node group. Required unless `enable_autoscale` is `true`. Changing this resizes the node group. While autoscaling is enabled, the node count is managed by the autoscaler: its changes and changes of `nodes_count` in the configuration are ignored.

* `cloud_nodegroup_config` - (Required) Cloud server configuration of the nodes. Changing any of its values creates a new node group. Learn more about [Configurations](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/configurations/).

  * `flavor_id` - (Optional) Unique identifier of an OpenStack flavor for all nodes. Conflicts with `cpus` and `ram_mb`. Set either `flavor_id`, or `cpus` and `ram_mb`. Learn more about [Flavors](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/configurations/#create-node-group-with-prebuilt-cloud-server-configuration).

  * `cpus` - (Optional) Number of vCPUs for each node. Requires `ram_mb`.

  * `ram_mb` - (Optional) Amount of RAM in MB for each node. Requires `cpus`.

  * `volume_gb` - (Optional) Volume size in GB for each node. Cannot be set together with `flavor_id` when `local_volume` is `true`: such a flavor defines the volume size itself.

  * `volume_type` - (Optional) Type of an OpenStack Block Storage volume for each node in the `<volume_type>.<segment>` format, for example, `fast.ru-7a`. Available volume types are `fast`, `basic`, and `universal`. Cannot be set when `local_volume` is `true`. Learn more about [Network volumes](https://docs.selectel.ru/en/cloud/servers/volumes/about-network-volumes/).

  * `local_volume` - (Optional) Specifies if nodes use a local volume instead of a network one. Boolean flag.

  * `affinity_policy` - (Optional) Affinity policy of the nodes. Available values are `soft-anti-affinity` and `soft-affinity`. If omitted, `soft-anti-affinity` is used.

* `cidr` - (Optional) CIDR of the node group network. Changing this creates a new node group.

* `labels` - (Optional) Kubernetes labels applied to each node in the node group. Changing this updates the nodes in place. A label removed from the configuration stays; set an empty map to remove all labels.

* `taints` - (Optional) Kubernetes taints applied to each node in the node group. Changing this updates the nodes in place. A taint removed from the configuration stays; set an empty list to remove all taints. Learn more about [Taints](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/add-taints/).

  * `key` - (Required) Taint key.

  * `value` - (Required) Taint value.

  * `effect` - (Required) Taint effect. Available values are `NoSchedule`, `PreferNoSchedule`, and `NoExecute`.

* `enable_autoscale` - (Optional) Enables or disables autoscaling of the node group. Boolean flag. Learn more about [Autoscaling](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/cluster-autoscaler/).

* `autoscale_min_nodes` - (Optional) Minimum number of worker nodes while autoscaling is enabled.

* `autoscale_max_nodes` - (Optional) Maximum number of worker nodes while autoscaling is enabled.

* `user_data` - (Optional) Base64-encoded script that worker nodes run on the first boot. Changing this creates a new node group. Learn more about [User data](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/user-data/).

* `install_nvidia_device_plugin` - (Optional) Enables or disables installation of the NVIDIA Device Plugin and GPU drivers. Changing this creates a new node group. If omitted, it is enabled for flavors with GPU.

* `preemptible` - (Optional) Enables or disables the use of preemptible nodes. Changing this creates a new node group. Boolean flag, the default value is `false`. Learn more about [Preemptible node groups](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/preemptible-node-groups/).

* `timeouts` - (Optional) Timeouts of the `create`, `update` and `delete` operations, for example, `30m`. The default value of each is `60m`.

## Attributes Reference

* `id` - Unique identifier of the node group in the `<cluster_id>/<nodegroup_id>` format.

* `nodegroup_type` - Type of the node group, for example, `STANDARD` or `GPU`.

* `status` - Node group status as the API returns it.

* `nodes` - List of nodes in the node group.

  * `id` - Unique identifier of the node.

  * `ip` - IP address of the node.

  * `hostname` - Hostname of the node.

## Import

You can import a node group:

```shell
export OS_DOMAIN_NAME=<account_id>
export OS_USERNAME=<username>
export OS_PASSWORD=<password>
export INFRA_PROJECT_ID=<selectel_project_id>
export INFRA_REGION=<selectel_pool>
terraform import selectel_mks_nodegroup_v2.nodegroup_1 <cluster_id>/<nodegroup_id>
```

where:

* `<account_id>` — Selectel account ID. The account ID is in the top right corner of the [Control panel](https://my.selectel.ru/). Learn more about [Registration](https://docs.selectel.ru/en/control-panel-actions/account/registration/).

* `<username>` — Name of the service user. To get the name, in the [Control panel](https://my.selectel.ru/iam/users_management/users?type=service), go to **Identity & Access Management** ⟶ **User management** ⟶ the **Service users** tab ⟶ copy the name of the required user. Learn more about [Service users](https://docs.selectel.ru/en/control-panel-actions/users-and-roles/user-types-and-roles/).

* `<password>` — Password of the service user.

* `<selectel_project_id>` — Unique identifier of the associated project. To get the ID, in the [Control panel](https://my.selectel.ru/vpc/mks), go to **Cloud Platform** ⟶ project name ⟶ copy the ID of the required project. Learn more about [Projects](https://docs.selectel.ru/en/cloud/managed-kubernetes/about/projects/).

* `<selectel_pool>` — Pool where the cluster is located, for example, `ru-7`. To get information about the pool, in the [Control panel](https://my.selectel.ru/vpc/mks/), go to **Cloud Platform** ⟶ **Kubernetes**. The pool is in the **Pool** column.

* `<cluster_id>` — Unique identifier of the cluster, for example, `b311ce58-2658-46b5-b733-7a0f418703f2`. To get the cluster ID, in the [Control panel](https://my.selectel.ru/vpc/mks/), go to **Cloud Platform** ⟶ **Kubernetes** ⟶ the cluster page ⟶ copy the ID at the top of the page under the cluster name, near the region and pool.

* `<nodegroup_id>` — Unique identifier of the node group, for example, `63ed5342-b22c-4c7a-9d41-c1fe4a142c13`. To get the node group ID, in the [Control panel](https://my.selectel.ru/vpc/mks/), go to **Cloud Platform** ⟶ **Kubernetes**. Click the required cluster. The node group ID is at the top of the node group card, near the pool.

The API does not return `cloud_nodegroup_config.cpus`, `ram_mb`, `affinity_policy` and `cidr` of a cloud node group, so they stay empty after import. Setting them in the configuration of the imported node group fills the state on the next apply and does not create a new node group.
