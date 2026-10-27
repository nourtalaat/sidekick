terraform {
  required_providers {
    datadog = {
      source  = "DataDog/datadog"
      version = "3.0.0"
    }
  }
}

resource "datadog_monitor" "checkout_errors" {
  name    = "Checkout error rate"
  type    = "metric alert"
  message = "Checkout errors are high."
  query   = "sum(last_5m):sum:checkout.errors{env:prod}.as_count() > 50"
}
