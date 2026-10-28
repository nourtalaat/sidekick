terraform {
  required_providers {
    datadog = {
      source = "DataDog/datadog"
    }
  }
}

variable "name" {
  type = string
}

variable "threshold" {
  type = number
}

resource "datadog_monitor" "this" {
  name    = var.name
  type    = "metric alert"
  message = "${var.name} is above ${var.threshold}."
  query   = "sum(last_5m):sum:checkout.errors{env:prod}.as_count() > ${var.threshold}"

  monitor_thresholds {
    critical = var.threshold
  }
}
