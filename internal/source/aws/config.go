package aws

import (
	"errors"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/awsauth"
	"github.com/nourtalaat/sidekick/internal/source"
)

// logGroupPattern is the shape of a log group name, or the start of one.
var logGroupPattern = regexp.MustCompile(`^[\w./#-]{1,512}$`)

// Config is the config section of one AWS source.
type Config struct {
	source.Settings `yaml:",inline"`

	// Accounts are the AWS accounts the tools may read. A call for any
	// other account is a tool error, and nothing is sent.
	Accounts []Account `yaml:"accounts"`
	// Regions are the Regions the tools may read, in every account. A call
	// for any other Region is a tool error, and nothing is sent.
	Regions []string `yaml:"regions"`
	// LogGroups are the starts of the log group names query_logs may read,
	// for example /aws/eks/prod/ or /ecs/checkout. Empty means query_logs
	// reads no log group.
	LogGroups []string `yaml:"logGroups"`
}

// Account is one AWS account the source reads. Its roleARN is optional: an
// account without one is read with Sidekick's own credentials, which must
// belong to it.
type Account = awsauth.Account

// Validate checks the section without credentials or network calls. Each
// error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	errs = append(errs, awsauth.CheckAccounts(c.Accounts)...)
	errs = append(errs, awsauth.CheckRegions(c.Regions)...)
	errs = append(errs, awsauth.CheckList("logGroups", c.LogGroups, logGroupPattern,
		"the start of a log group name, of letters, digits and _ - / . #")...)

	return errors.Join(errs...)
}

// allowedLogGroup reports whether name, a log group name or the start of
// one, starts with one of the configured prefixes.
func (c *Config) allowedLogGroup(name string) bool {
	for _, prefix := range c.LogGroups {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}

	return false
}
