---
page_title: "Selectel: selectel_mks_admission_controllers_v2"
description: |-
  Provides a list of admission controllers available in Selectel Managed Kubernetes using API v2.
---

# selectel\_mks\_admission_controllers_v2

Provides a list of available admission controllers. For more information about admission controllers in Managed Kubernetes, see the [official Selectel documentation](https://docs.selectel.ru/en/cloud/managed-kubernetes/clusters/admission-controllers/).

The data source uses Managed Kubernetes API v2 and returns the same data as `selectel_mks_admission_controllers_v1`, with the `region` argument renamed to `pool`.

## Example Usage

```terraform
data "selectel_mks_admission_controllers_v2" "admission_controllers_1" {
  project_id = selectel_vpc_project_v2.project_1.id
  pool       = "ru-3"
  filter = [{
    kube_version = "1.31.2"
  }]
}
```

## Argument Reference

* `project_id` - (Optional) Unique identifier of the associated project. If omitted, the `project_id` of the provider configuration is used. Retrieved from the [selectel_vpc_project_v2](https://registry.terraform.io/providers/selectel/selectel/latest/docs/resources/vpc_project_v2) resource. Learn more about [Projects](https://docs.selectel.ru/en/control-panel-actions/projects/about-projects/).

* `pool` - (Required) Pool where the cluster is located, for example, `ru-3`. Learn more about available pools in [Product availability by location](https://docs.selectel.ru/en/infrastructure/product-availability-by-location/).

* `filter` - (Optional) Values to filter available admission controllers, for example, `filter = [{ kube_version = "1.31.2" }]`:

  * `kube_version` - (Required) Kubernetes version for which you get available admission controllers.

## Attributes Reference

* `id` - Checksum of the returned list.

* `admission_controllers` - List of available admission controllers.

  * `kube_version` - Kubernetes version.

  * `names` - Names of the admission controllers available for the specified Kubernetes version.
