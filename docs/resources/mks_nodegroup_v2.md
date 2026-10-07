---
page_title: "Selectel: selectel_mks_nodegroup_v2"
description: |-
  Creates and manages a node group of cloud or dedicated servers in Selectel Managed Kubernetes using API v2.
---

# selectel\_mks\_nodegroup\_v2

Creates and manages a Managed Kubernetes node group of cloud or dedicated servers using API v2. For more information about node groups, see the [official Selectel documentation](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/).

A node group is either a cloud one, with `cloud_nodegroup_config`, or a dedicated one, with `dedicated_nodegroup_config`. Set exactly one of them. The `workers_type` of the cluster decides which one is allowed: a `CLOUD` cluster accepts only cloud node groups, a `DEDICATED` cluster accepts only dedicated servers, and changing `workers_type` creates a new cluster.

The node group takes the project from the provider configuration: set `project_id` in the provider or the `INFRA_PROJECT_ID` environment variable. The pool of a cloud node group is derived from `segment`. A dedicated node group takes the pool from the provider configuration: set `region` in the provider or the `INFRA_REGION` environment variable to the pool of the cluster.

The resource waits for the node group tasks of every operation. When a task fails, the operation fails with the task type, ID and error details.

## Example Usage

### Cloud node group

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

### Dedicated node group

The cluster has `workers_type = "DEDICATED"`, and the server configuration comes from the [selectel_dedicated_configuration_v1](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/dedicated_configuration_v1) data source:

```terraform
data "selectel_dedicated_configuration_v1" "server_config" {
  project_id  = selectel_vpc_project_v2.project_1.id
  deep_filter = "{\"name\":\"CL25-NVMe\"}"
}

resource "selectel_mks_cluster_v2" "cluster_dedicated" {
  name         = "cluster-dedicated"
  project_id   = selectel_vpc_project_v2.project_1.id
  pool         = "ru-1"
  kube_version = "1.31.4"
  workers_type = "DEDICATED"
}

resource "selectel_mks_nodegroup_v2" "nodegroup_dedicated" {
  cluster_id  = selectel_mks_cluster_v2.cluster_dedicated.id
  segment     = "SPB-3"
  nodes_count = 2
  cidr        = "10.20.30.0/24"

  dedicated_nodegroup_config = {
    service_uuid    = data.selectel_dedicated_configuration_v1.server_config.configurations[0].id
    price_plan_name = "1 month"
    root_size_gb    = 100
  }

  labels = {
    "label-key0" = "label-value0"
  }
}
```

## Argument Reference

* `cluster_id` - (Required) Unique identifier of the cluster. Changing this creates a new node group. Retrieved from the [selectel_mks_cluster_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/mks_cluster_v2) resource. A cluster with `workers_type = "CLOUD"` accepts only `cloud_nodegroup_config`, a cluster with `workers_type = "DEDICATED"` accepts only `dedicated_nodegroup_config`. A node group of the other kind fails at plan, or before any request when the cluster is created in the same apply.

* `segment` - (Required) Pool segment where all nodes of a cloud node group are located, for example, `ru-7a`. For a dedicated node group, the dedicated server location, for example, `SPB-3`. Changing this creates a new node group. Learn more about available pool segments in the [Availability matrix](https://docs.selectel.ru/en/control-panel-actions/availability-matrix/#managed-kubernetes).

* `nodes_count` - (Optional) Number of worker nodes in the node group. Required unless `enable_autoscale` is `true`, and always required for a dedicated node group. Changing this resizes the node group. While autoscaling is enabled, the node count is managed by the autoscaler: its changes and changes of `nodes_count` in the configuration are ignored.

* `cloud_nodegroup_config` - (Optional) Cloud server configuration of the nodes. Conflicts with `dedicated_nodegroup_config`. Changing any of its values creates a new node group. Learn more about [Configurations](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/configurations/).

  * `flavor_id` - (Optional) Unique identifier of an OpenStack flavor for all nodes. Conflicts with `cpus` and `ram_mb`. Set either `flavor_id`, or `cpus` and `ram_mb`. Learn more about [Flavors](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/configurations/#create-node-group-with-prebuilt-cloud-server-configuration).

  * `cpus` - (Optional) Number of vCPUs for each node. Requires `ram_mb`.

  * `ram_mb` - (Optional) Amount of RAM in MB for each node. Requires `cpus`.

  * `volume_gb` - (Optional) Volume size in GB for each node. Omit it with `flavor_id`: the API takes it from a flavor that defines a volume, and a different configured value fails the apply. Cannot be set together with `flavor_id` when `local_volume` is `true`.

  * `volume_type` - (Optional) Type of an OpenStack Block Storage volume for each node in the `<volume_type>.<segment>` format, for example, `fast.ru-7a`. Available volume types are `fast`, `basic`, and `universal`. Cannot be set when `local_volume` is `true`. Learn more about [Network volumes](https://docs.selectel.ru/en/cloud/servers/volumes/about-network-volumes/).

  * `local_volume` - (Optional) Specifies if nodes use a local volume instead of a network one. Boolean flag. Omit it with `flavor_id`: the API sets it for a flavor with a local disk, and a different configured value fails the apply.

  * `affinity_policy` - (Optional) Affinity policy of the nodes. Available values are `soft-anti-affinity` and `soft-affinity`. If omitted, `soft-anti-affinity` is used.

* `dedicated_nodegroup_config` - (Optional) Dedicated server configuration of the nodes. Conflicts with `cloud_nodegroup_config`. Changing any of its values creates a new node group. A dedicated node group does not support autoscaling, so `enable_autoscale`, `autoscale_min_nodes` and `autoscale_max_nodes` cannot be set, and it cannot be preemptible.

  * `service_uuid` - (Required) Unique identifier of the dedicated server configuration. Retrieved from the `configurations[].id` attribute of the [selectel_dedicated_configuration_v1](https://registry.terraform.io/providers/selectel/selectel/latest/docs/data-sources/dedicated_configuration_v1) data source.

  * `price_plan_name` - (Required) Name of the price plan of the servers. Available price plans are `1 day`, `1 month`, `3 months`, `6 months`, `12 months`, and `12 months • monthly payment`. The provider resolves the name to `price_plan_uuid` at plan, so an unknown name fails the plan. Learn more about price plans in the [Payment model and prices of a dedicated server](https://docs.selectel.ru/en/dedicated/about/payment).

  * `root_size_gb` - (Optional) Size of the root partition of each server in GB, at least `30`. If omitted, `100` is used.

  * `create_storage_partition` - (Optional) Creates a storage partition on the fastest disk of each server. Boolean flag, the default value is `true`.

  * `currency` - (Optional) Balance that pays for the servers. Available values are `main` and `bonus`. If omitted, `main` is used.

* `cidr` - (Optional) CIDR of the dedicated node group network, a private `/24` network, for example, `10.20.30.0/24`. Changing this creates a new node group. Applies to dedicated node groups only: setting it for a cloud node group fails the plan.

* `labels` - (Optional) Kubernetes labels applied to each node in the node group. Changing this updates the nodes in place. Removing the `labels` argument keeps the current labels; set an empty map to remove them.

* `taints` - (Optional) Kubernetes taints applied to each node in the node group. Changing this updates the nodes in place. Removing the `taints` argument keeps the current taints; set an empty list to remove them. Learn more about [Taints](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/add-taints/).

  * `key` - (Required) Taint key.

  * `value` - (Required) Taint value.

  * `effect` - (Required) Taint effect. Available values are `NoSchedule`, `PreferNoSchedule`, and `NoExecute`.

* `enable_autoscale` - (Optional) Enables or disables autoscaling of the node group. Boolean flag. Requires `autoscale_min_nodes` and `autoscale_max_nodes` when `true`. Cloud node groups only. Learn more about [Autoscaling](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/cluster-autoscaler/).

* `autoscale_min_nodes` - (Optional) Minimum number of worker nodes while autoscaling is enabled.

* `autoscale_max_nodes` - (Optional) Maximum number of worker nodes while autoscaling is enabled.

* `user_data` - (Optional) Base64-encoded script that worker nodes run on the first boot. Changing this creates a new node group. Learn more about [User data](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/user-data/).

* `install_nvidia_device_plugin` - (Optional) Enables or disables installation of the NVIDIA Device Plugin and GPU drivers. Changing this creates a new node group. If omitted, it is enabled for flavors and dedicated servers with GPU. Cannot be `true` with `cpus` and `ram_mb`: set a GPU `flavor_id`.

* `preemptible` - (Optional) Enables or disables the use of preemptible nodes. Changing this creates a new node group. Boolean flag, the default value is `false`. Cloud node groups only. Learn more about [Preemptible node groups](https://docs.selectel.ru/en/cloud/managed-kubernetes/node-groups/preemptible-node-groups/).

* `timeouts` - (Optional) Timeouts of the `create`, `update` and `delete` operations, for example, `30m`. The default value of each is `60m`, except `create` of a dedicated node group, `160m`: ordering and installing the servers can take that long.

## Attributes Reference

* `id` - Unique identifier of the node group in the `<cluster_id>/<nodegroup_id>` format.

* `nodegroup_type` - Type of the node group: `STANDARD`, `GPU`, `DEDICATED` or `DEDICATED_GPU`.

* `dedicated_nodegroup_config.price_plan_uuid` - Unique identifier of the price plan that `price_plan_name` names.

* `status` - Node group status as the API returns it.

* `nodes` - List of nodes in the node group.

  * `id` - Unique identifier of the node.

  * `ip` - IP address of the node.

  * `hostname` - Hostname of the node.

## Moving from selectel\_mks\_nodegroup\_v1

In Terraform 1.8.0 and later, a [`moved` block](https://developer.hashicorp.com/terraform/language/moved) moves `selectel_mks_nodegroup_v1` in the state to `selectel_mks_nodegroup_v2` without changing the node group itself. Replace the `selectel_mks_nodegroup_v1` resource in the configuration with an equivalent `selectel_mks_nodegroup_v2` one and add:

```terraform
moved {
  from = selectel_mks_nodegroup_v1.nodegroup_1
  to   = selectel_mks_nodegroup_v2.nodegroup_1
}
```

Move the node groups together with their cluster. For the mapping of the arguments, earlier Terraform versions and a complete example, see the [migration guide](https://registry.terraform.io/providers/selectel/selectel/latest/docs/guides/migrating_mks_to_v2).

## Import

### In Terraform 1.12.0 and later

Use the [`import` block](https://developer.hashicorp.com/terraform/language/import) with the `identity` attribute. The project comes from the provider configuration:

```terraform
import {
  to = selectel_mks_nodegroup_v2.nodegroup_1
  identity = {
    cluster_id = "<cluster_id>"
    id         = "<nodegroup_id>"
    pool       = "<selectel_pool>"
  }
}
```

### In Terraform 1.5.0 and later

Use the [`import` block](https://developer.hashicorp.com/terraform/language/import) with the `id` attribute. The pool comes from the provider configuration, as for the `terraform import` command.

```terraform
import {
  to = selectel_mks_nodegroup_v2.nodegroup_1
  id = "<cluster_id>/<nodegroup_id>"
}
```

### All versions

Use the [`terraform import` command](https://developer.hashicorp.com/terraform/cli/commands/import):

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

The API does not return `cloud_nodegroup_config.cpus`, `ram_mb` and `affinity_policy` of a cloud node group, so they stay empty after import. Setting them in the configuration of the imported node group fills the state on the first apply after the import and does not create a new node group. Once that apply or the next one has run, setting a value still missing from the state creates a new node group.

Of a dedicated node group, the API does not return `currency`, so it stays empty after import. The import fills `price_plan_name` from the price plan of the node group.
