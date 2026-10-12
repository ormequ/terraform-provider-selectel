data "selectel_mks_kube_versions_v2" "versions" {
  project_id = selectel_vpc_project_v2.project_1.id
  pool       = "ru-7"
}

resource "selectel_mks_cluster_v2" "cluster_1" {
  name         = "cluster-1"
  project_id   = selectel_vpc_project_v2.project_1.id
  pool         = "ru-7"
  kube_version = data.selectel_mks_kube_versions_v2.versions.latest_version
  workers_type = "CLOUD"
  cni_type     = "CILIUM"
  cni_cilium_settings = {
    envoy_daemonset = false
    hubble_relay    = true
  }
  kubernetes_options = {
    feature_gates = ["TopologyAwareHints"]
  }
}
