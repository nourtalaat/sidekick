terraform {
  required_providers {
    grafana = {
      source  = "grafana/grafana"
      version = "~> 4.0"
    }
  }
}

resource "grafana_folder" "payments" {
  uid = "payments"
}
