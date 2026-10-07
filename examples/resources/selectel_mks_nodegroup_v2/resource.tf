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
}
