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
