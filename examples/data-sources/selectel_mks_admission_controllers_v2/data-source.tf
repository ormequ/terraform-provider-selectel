data "selectel_mks_admission_controllers_v2" "admission_controllers_1" {
  project_id = selectel_vpc_project_v2.project_1.id
  pool       = "ru-3"
  filter = [{
    kube_version = "1.31.2"
  }]
}
