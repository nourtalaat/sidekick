terraform {
  required_providers {
    signoz = {
      source  = "SigNoz/signoz"
      version = "~> 0.1"
    }
  }
}

resource "signoz_notification_channel" "payments" {
  name         = "payments-webhook"
  display_name = "payments-webhook"
  config = {
    webhook = {
      kind = "webhook"
      spec = {
        url           = "https://example.com/payments"
        send_resolved = true
      }
    }
  }
}
