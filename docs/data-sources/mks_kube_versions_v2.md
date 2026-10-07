---
page_title: "Selectel: selectel_mks_kube_versions_v2"
description: |-
  Provides a list of supported Kubernetes versions for a Selectel Managed Kubernetes cluster using API v2.
---

# selectel\_mks\_kube_versions_v2

Provides a list of supported Kubernetes versions for a Managed Kubernetes cluster. For more information about Managed Kubernetes, see the [official Selectel documentation](https://docs.selectel.ru/en/cloud/managed-kubernetes/about/about-managed-kubernetes/).

The data source uses Managed Kubernetes API v2 and returns the same data as `selectel_mks_kube_versions_v1`, with the `region` argument renamed to `pool`.

## Example Usage

```terraform
data "selectel_mks_kube_versions_v2" "versions" {
  project_id = selectel_vpc_project_v2.project_1.id
  pool       = "ru-3"
}

output "latest_version" {
  value = data.selectel_mks_kube_versions_v2.versions.latest_version
}

output "default_version" {
  value = data.selectel_mks_kube_versions_v2.versions.default_version
}

output "versions" {
  value = data.selectel_mks_kube_versions_v2.versions.versions
}
```

## Argument Reference

* `project_id` - (Optional) Unique identifier of the associated project. If omitted, the `project_id` of the provider configuration is used. Retrieved from the [selectel_vpc_project_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/vpc_project_v2) resource. Learn more about [Projects](https://docs.selectel.ru/en/control-panel-actions/projects/about-projects/).

* `pool` - (Required) Pool where the cluster is located, for example, `ru-3`. Learn more about available pools in [Product availability by location](https://docs.selectel.ru/en/infrastructure/product-availability-by-location/).

## Attributes Reference

* `id` - Checksum of the version list.

* `latest_version` - The most recent version.

* `default_version` - Kubernetes version used by default.

* `versions` - List of the supported versions.
