module "checkout" {
  source    = "../modules/monitor"
  name      = "Checkout error rate"
  threshold = 50
}
