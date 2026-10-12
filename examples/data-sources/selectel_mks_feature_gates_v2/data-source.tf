data "selectel_mks_feature_gates_v2" "feature_gates_1" {
  project_id = selectel_vpc_project_v2.project_1.id
  pool       = "ru-3"
  filter = [{
    kube_version = "1.31.2"
  }]
}
