resource "selectel_mks_cluster_v2" "cluster_1" {
  name         = "cluster-1"
  project_id   = selectel_vpc_project_v2.project_1.id
  region       = "ru-3"
  kube_version = "1.33.4"

  oidc = {
    enabled    = true
    issuer_url = "https://issuer.example.com"
  }
}
