---
page_title: "Selectel: selectel_mks_kubeconfig_v2"
description: |-
  Provides a kubeconfig file and its fields for a Selectel Managed Kubernetes cluster using API v2.
---

# selectel\_mks\_kubeconfig_v2

Provides a kubeconfig file and its fields for a Managed Kubernetes cluster. For more information about Managed Kubernetes, see the [official Selectel documentation](https://docs.selectel.ru/en/cloud/managed-kubernetes/).

The data source uses Managed Kubernetes API v2 and returns the same data as `selectel_mks_kubeconfig_v1`, with the `region` argument renamed to `pool`.

## Example Usage

```terraform
data "selectel_mks_kubeconfig_v2" "kubeconfig" {
  cluster_id = selectel_mks_cluster_v1.cluster_1.id
  project_id = selectel_mks_cluster_v1.cluster_1.project_id
  pool       = selectel_mks_cluster_v1.cluster_1.region
}

provider "kubernetes" {
  host                   = data.selectel_mks_kubeconfig_v2.kubeconfig.server
  client_certificate     = base64decode(data.selectel_mks_kubeconfig_v2.kubeconfig.client_cert)
  client_key             = base64decode(data.selectel_mks_kubeconfig_v2.kubeconfig.client_key)
  cluster_ca_certificate = base64decode(data.selectel_mks_kubeconfig_v2.kubeconfig.cluster_ca_cert)
}

output "kubeconfig" {
  value     = data.selectel_mks_kubeconfig_v2.kubeconfig.raw_config
  sensitive = true
}
```

## Argument Reference

* `cluster_id` - (Required) Unique identifier of the cluster.

* `project_id` - (Optional) Unique identifier of the associated project. If omitted, the `project_id` of the provider configuration is used. Retrieved from the [selectel_vpc_project_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/vpc_project_v2) resource. Learn more about [Projects](https://docs.selectel.ru/en/control-panel-actions/projects/about-projects/).

* `pool` - (Required) Pool where the cluster is located, for example, `ru-3`. Learn more about available pools in [Product availability by location](https://docs.selectel.ru/en/infrastructure/product-availability-by-location/).

## Attributes Reference

* `id` - Unique identifier of the cluster.

* `raw_config` - Raw content of a kubeconfig file.

* `server` - IP address and port for a Kube API server.

* `cluster_ca_cert` - CA certificate of the cluster.

* `client_key` - Client key for authorization.

* `client_cert` - Client certificate for authorization.
