terraform {
  required_providers {
    datadog = {
      source  = "DataDog/datadog"
      version = "~> 4.0"
    }
  }
}

resource "datadog_monitor" "checkout_errors" {
  name    = "Checkout error rate"
  type    = "metric alert"
  message = "Checkout errors are high. @slack-payments"
  query   = "sum(last_5m):sum:checkout.errors{env:prod}.as_count() > 50"

  monitor_thresholds {
    critical = 50
    warning  = 20
  }

  tags = ["team:payments"]
}
