---
page_title: "Migrating Managed Kubernetes resources from _v1 to _v2"
description: |-
  How to move selectel_mks_cluster_v1 and selectel_mks_nodegroup_v1 to selectel_mks_cluster_v2 and selectel_mks_nodegroup_v2 without recreating the cluster.
---

# Migrating Managed Kubernetes resources from _v1 to _v2

`selectel_mks_cluster_v2` and `selectel_mks_nodegroup_v2` manage Managed Kubernetes clusters and cloud node groups through API v2. They work with the same clusters and node groups as `selectel_mks_cluster_v1` and `selectel_mks_nodegroup_v1`, so you can switch an existing cluster to the `_v2` resources without recreating it:

- in Terraform 1.8.0 and later, with [`moved` blocks](https://developer.hashicorp.com/terraform/language/moved) — the provider converts the `_v1` state itself;
- in earlier versions, by removing the `_v1` resources from the state and importing the `_v2` ones.

Both ways change only the Terraform state. The cluster, its node groups and nodes stay as they are.

Before you start, upgrade to a provider version that has the `_v2` resources and ensure that `terraform plan` shows no changes for your `_v1` resources.

## What changes in the configuration

### Cluster

| `selectel_mks_cluster_v1` | `selectel_mks_cluster_v2` | Note |
|---|---|---|
| `name` | `name` | The API stores the name in lower case, as in `_v1`: a name that differs only in letter case is the same name. |
| `region` | `pool` | |
| `project_id` | `project_id` | Optional in `_v2`: if omitted, the `project_id` of the provider configuration is used. |
| `kube_version` | `kube_version` | Only the `x.y.z` format, without the `v` prefix. The cluster may run a newer version than configured, for example after patch auto-upgrade: a lower configured version is accepted and never downgrades the cluster. |
| `cluster_type`, `zonal` | `cluster_type` | `zonal = true` is `cluster_type = "BASIC"`, `zonal = false` is `cluster_type = "HIGH_AVAILABILITY"`. If both are set, `cluster_type` wins. `zonal` is gone. Upper case only, unlike `_v1`: `BASIC`, `HIGH_AVAILABILITY` or `HIGH_AVAILABILITY_MULTI_AZ`. |
| — | `workers_type` | Required. Set `"CLOUD"` for a cluster created by `selectel_mks_cluster_v1`. |
| `network_id`, `subnet_id` | `network_id`, `subnet_id` | |
| — | `cloud_subnet_cidr` | New, optional. Do not set it for an existing cluster: changing it recreates the cluster. |
| `private_kube_api` | `private_kube_api` | |
| `maintenance_window_start` | `maintenance_window_start` | |
| `enable_autorepair` | `enable_autorepair` | |
| `enable_patch_version_auto_upgrade` | `enable_patch_version_auto_upgrade` | |
| `enable_pod_security_policy` | — | Removed: API v2 does not support it. Remove it from the configuration. |
| `cni_type` | `cni_type` | Upper case only, unlike `_v1`: `CALICO` or `CILIUM`. |
| `cni_cilium_settings { ... }` | `cni_cilium_settings = { ... }` | A nested attribute, so it is written with `=`. |
| `feature_gates` | `kubernetes_options.feature_gates` | |
| `admission_controllers` | `kubernetes_options.admission_controllers` | |
| `enable_audit_logs` | `kubernetes_options.audit_logs.enabled` | `audit_logs` also has `secret_name`. |
| `oidc { ... }` | `kubernetes_options.oidc = { ... }` | The same nested arguments. |
| — | `kubernetes_options.x509_ca_certificates` | New, optional. |
| `kube_api_ip`, `status`, `maintenance_window_end` | the same | Read-only. |
| `timeouts { ... }` | `timeouts { ... }` | Same syntax and defaults. Timeouts are not moved: add the block to the `_v2` resource again if you need it. |

### Node group

| `selectel_mks_nodegroup_v1` | `selectel_mks_nodegroup_v2` | Note |
|---|---|---|
| `cluster_id` | `cluster_id` | Point it at the `selectel_mks_cluster_v2` resource. |
| `project_id` | — | Removed: the node group takes the project from the provider configuration. Set `project_id` in the provider or the `INFRA_PROJECT_ID` environment variable. It must equal the former `project_id` of each node group. |
| `region` | — | Removed: the pool follows from `segment`. |
| `availability_zone` | `segment` | |
| `nodes_count` | `nodes_count` | |
| `flavor_id`, `cpus`, `ram_mb`, `volume_gb`, `volume_type`, `local_volume`, `affinity_policy` | `cloud_nodegroup_config = { ... }` | The same names inside the nested attribute. |
| `keypair_name` | — | Removed: API v2 ignores it. Remove it from the configuration. |
| — | `cidr` | For dedicated node groups only, which `selectel_mks_nodegroup_v2` does not support yet: setting it fails the plan. Do not set it. |
| `labels` | `labels` | |
| `taints { ... }` | `taints = [{ ... }]` | A list of objects instead of repeated blocks. |
| `enable_autoscale`, `autoscale_min_nodes`, `autoscale_max_nodes` | the same | |
| `user_data`, `install_nvidia_device_plugin`, `preemptible` | the same | `install_nvidia_device_plugin` is optional in `_v2`. |
| `nodegroup_type`, `nodes`, `status` | the same | Read-only. |
| `timeouts { ... }` | `timeouts { ... }` | Not moved, as for the cluster. |

## Example

Before:

```hcl
resource "selectel_mks_cluster_v1" "cluster_1" {
  name                              = "cluster-1"
  project_id                        = selectel_vpc_project_v2.project_1.id
  region                            = "ru-7"
  kube_version                      = "1.30.3"
  zonal                             = false
  enable_patch_version_auto_upgrade = true
  feature_gates                     = ["TopologyAwareHints"]
  enable_audit_logs                 = true
}

resource "selectel_mks_nodegroup_v1" "nodegroup_1" {
  cluster_id        = selectel_mks_cluster_v1.cluster_1.id
  project_id        = selectel_mks_cluster_v1.cluster_1.project_id
  region            = selectel_mks_cluster_v1.cluster_1.region
  availability_zone = "ru-7a"
  nodes_count       = 3
  cpus              = 2
  ram_mb            = 4096
  volume_gb         = 32
  volume_type       = "fast.ru-7a"
  keypair_name      = "ssh-key"
  labels = {
    "label-key0" = "label-value0"
  }
  taints {
    key    = "test-key-0"
    value  = "test-value-0"
    effect = "NoSchedule"
  }
  install_nvidia_device_plugin = false
}
```

After:

```hcl
provider "selectel" {
  # ...
  # selectel_mks_nodegroup_v2 takes the project from here.
  project_id = "<selectel_project_id>"
}

moved {
  from = selectel_mks_cluster_v1.cluster_1
  to   = selectel_mks_cluster_v2.cluster_1
}

moved {
  from = selectel_mks_nodegroup_v1.nodegroup_1
  to   = selectel_mks_nodegroup_v2.nodegroup_1
}

resource "selectel_mks_cluster_v2" "cluster_1" {
  name                              = "cluster-1"
  project_id                        = selectel_vpc_project_v2.project_1.id
  pool                              = "ru-7"
  kube_version                      = "1.30.3"
  cluster_type                      = "HIGH_AVAILABILITY"
  workers_type                      = "CLOUD"
  enable_patch_version_auto_upgrade = true
  kubernetes_options = {
    feature_gates = ["TopologyAwareHints"]
    audit_logs = {
      enabled = true
    }
  }
}

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
  install_nvidia_device_plugin = false
}
```

## Move with `moved` blocks (Terraform 1.8.0 and later)

1. In the configuration, replace every `selectel_mks_cluster_v1` and `selectel_mks_nodegroup_v1` resource with the equivalent `_v2` resource, as in the tables above. Keep the resource names, or use new ones in the `to` addresses.
2. Add a `moved` block for the cluster and for each of its node groups. Move a cluster together with all its node groups in the same run.
3. If the configuration has no provider-level `project_id`, add it, or set the `INFRA_PROJECT_ID` environment variable: `selectel_mks_nodegroup_v2` has no `project_id` argument. The provider project must equal the former `project_id` of each node group; otherwise the plan fails with `provider project ... is not the node group's project ... (moved from _v1)`.
4. Run `terraform plan`. Every resource must show `has moved to` and no changes. If the plan shows changes, compare the `_v2` arguments with the cluster: the values that `_v1` kept in the state are now compared with the `_v2` configuration.
5. Run `terraform apply` to save the moved state. No API changes are made.
6. You can delete the `moved` blocks after every workspace that uses the configuration has applied them. A module used by others should keep them.

The provider moves the state only from the `_v1` resources of the Selectel provider. A `moved` block between different providers fails with `Unable to Move Resource State`.

### What the first refresh fills in

The move converts the `_v1` state without API calls. The values `_v1` never stored are filled in by the refresh of the same `terraform plan`:

- `workers_type` — from the network type of the cluster: `CLOUD` for a cluster created by `selectel_mks_cluster_v1`;
- `kubernetes_options.audit_logs.secret_name` and the other Kubernetes options — from the cluster;
- `cluster_type` — from the cluster, when the `_v1` state has neither `cluster_type` nor `zonal`.

These stay empty, because the API does not return them:

- `cloud_subnet_cidr` and `kubernetes_options.x509_ca_certificates` of the cluster.

`cloud_nodegroup_config.cpus`, `ram_mb` and `affinity_policy` keep the values the `_v1` state had.

Dropped from the cluster: `enable_pod_security_policy`, `zonal`, `timeouts`. Dropped from the node group: `keypair_name`, `project_id`, `region`, `timeouts`.

## Remove and import (Terraform earlier than 1.8.0)

1. Remove the `_v1` resources from the state without destroying them:
   - in Terraform 1.7.x, replace each `_v1` resource in the configuration with a `removed` block:

     ```hcl
     removed {
       from = selectel_mks_nodegroup_v1.nodegroup_1

       lifecycle {
         destroy = false
       }
     }

     removed {
       from = selectel_mks_cluster_v1.cluster_1

       lifecycle {
         destroy = false
       }
     }
     ```

     and run `terraform apply`;
   - in earlier versions, run `terraform state rm selectel_mks_nodegroup_v1.nodegroup_1 selectel_mks_cluster_v1.cluster_1`.
2. Add the `_v2` resources to the configuration, as in the tables above.
3. Import them with `import` blocks (Terraform 1.5.0 and later) or the `terraform import` command. See the **Import** sections of [selectel_mks_cluster_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/mks_cluster_v2) and [selectel_mks_nodegroup_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/mks_nodegroup_v2).
4. Run `terraform plan`. It shows the imports and, for a node group that sets `cpus`, `ram_mb` or `affinity_policy`, an in-place update that only fills these values in the state, with no API change.

An import, unlike a move, does not know `cloud_nodegroup_config.cpus`, `ram_mb` and `affinity_policy` of a node group: the API does not return them. After the import, setting them in the configuration fills the state on the next `terraform apply` and does not recreate the node group. `cloud_subnet_cidr` and `kubernetes_options.x509_ca_certificates` of the cluster stay empty.
