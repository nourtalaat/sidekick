// Package aws is the native AWS source. It registers itself as the source
// type "aws".
//
// A source reads the AWS accounts and Regions its config lists, through
// CloudWatch Logs Insights, CloudWatch metrics and alarms, CloudTrail event
// history, EKS, ECS and AWS Health, with aws-sdk-go-v2. Credentials come from the default AWS chain (IRSA or EKS
// Pod Identity on EKS), and for each account that names a role, from that
// role, assumed with an optional external ID.
//
// Every tool only reads. A call for an account, Region or log group the
// config does not allow is a tool error before anything is sent, and every
// result links to the AWS console pages a person can open to check it.
package aws
