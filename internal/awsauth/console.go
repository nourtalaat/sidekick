package awsauth

import "strings"

// ConsoleRoot returns the address of the AWS console of region, in its
// partition, for the links a result or an action points a person to.
func ConsoleRoot(region string) string {
	switch {
	case strings.HasPrefix(region, "cn-"):
		return "https://" + region + ".console.amazonaws.cn"
	case strings.HasPrefix(region, "us-gov-"):
		return "https://" + region + ".console.amazonaws-us-gov.com"
	default:
		return "https://" + region + ".console.aws.amazon.com"
	}
}
