package datadogdowntime

import (
	"github.com/nourtalaat/sidekick/internal/registry"
)

// proposalID is the proposal the tests run, and otherProposalID another one.
const (
	proposalID      = "0194f2c8-7d5e-7a41-9a3b-1c2d3e4f5a6b"
	otherProposalID = "0194f2c8-7d5e-7a41-9a3b-ffffffffffff"
)

// The monitor listed in the limits of the tests, and their scopes.
const (
	listedMonitor = 123
	prodScope     = "env:prod"
	stagingScope  = "env:staging AND service:checkout"
)

// testConfig is a valid config for the action at url.
func testConfig(url string) Config {
	return Config{
		URL:          url,
		APIKeySecret: registry.SecretRef{Name: "datadog-downtime", Key: "apiKey"},
		AppKeySecret: registry.SecretRef{Name: "datadog-downtime", Key: "appKey"},
		Limits: Limits{
			MonitorIDs:  []int64{listedMonitor},
			MonitorTags: []string{"team:payments"},
			Scopes:      []string{prodScope, stagingScope},
		},
	}
}
