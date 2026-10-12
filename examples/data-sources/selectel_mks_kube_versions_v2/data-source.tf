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
