package aws

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxLogGroupLen is the longest log group name CloudWatch Logs allows.
const maxLogGroupLen = 512

var (
	// accountPattern is the shape of an AWS account ID.
	accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
	// regionPattern is the shape of an AWS Region code, such as eu-west-1
	// or us-gov-west-1.
	regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	// roleARNPattern is the shape of an IAM role ARN. The group is the
	// role's account.
	roleARNPattern = regexp.MustCompile(`^arn:aws(-[a-z]+)*:iam::([0-9]{12}):role/[\w+=,.@/-]{1,512}$`)
	// externalIDPattern is the shape of an external ID STS accepts.
	externalIDPattern = regexp.MustCompile(`^[\w+=,.@:/-]{2,1224}$`)
	// logGroupPattern is the shape of a log group name, or the start of
	// one.
	logGroupPattern = regexp.MustCompile(`^[\w./#-]{1,512}$`)
)

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

// Account is one AWS account the source reads.
type Account struct {
	// ID is the 12-digit account ID.
	ID string `yaml:"id"`
	// RoleARN is an IAM role in the account that Sidekick assumes for
	// every call there. Empty means Sidekick's own credentials, which must
	// belong to this account.
	RoleARN string `yaml:"roleARN"`
	// ExternalID is the external ID the role's trust policy asks for, if
	// any. It needs RoleARN.
	ExternalID string `yaml:"externalID"`
}

// Validate checks the section without credentials or network calls. Each
// error starts with the key of its field.
func (c *Config) Validate() error {
	var errs []error

	if len(c.Accounts) == 0 {
		errs = append(errs, errors.New("accounts: list at least one account"))
	}

	errs = append(errs, c.checkAccounts()...)

	if len(c.Regions) == 0 {
		errs = append(errs, errors.New("regions: list at least one Region"))
	}

	errs = append(errs, checkList("regions", c.Regions, regionPattern, "an AWS Region code such as us-east-1")...)
	errs = append(errs, checkList("logGroups", c.LogGroups, logGroupPattern,
		"the start of a log group name, of letters, digits and _ - / . #")...)

	return errors.Join(errs...)
}

// checkAccounts checks each account's ID and role, that no ID is listed
// twice, and that at most one account uses Sidekick's own credentials,
// which can belong to one account only.
func (c *Config) checkAccounts() []error {
	var errs []error

	seen := map[string]bool{}
	ownCredentials := ""

	for i, a := range c.Accounts {
		key := fmt.Sprintf("accounts[%d]", i)

		switch {
		case !accountPattern.MatchString(a.ID):
			errs = append(errs, fmt.Errorf("%s.id: %q is not a 12-digit AWS account ID; quote it in YAML", key, a.ID))
		case seen[a.ID]:
			errs = append(errs, fmt.Errorf("%s.id: %q is listed twice", key, a.ID))
		}

		seen[a.ID] = true

		if a.RoleARN == "" {
			if a.ExternalID != "" {
				errs = append(errs, fmt.Errorf("%s.externalID: needs roleARN", key))
			}

			if ownCredentials != "" {
				errs = append(errs, fmt.Errorf("%s.roleARN: required, because account %s already uses Sidekick's own credentials", key, ownCredentials))
			}

			ownCredentials = a.ID

			continue
		}

		m := roleARNPattern.FindStringSubmatch(a.RoleARN)

		switch {
		case m == nil:
			errs = append(errs, fmt.Errorf("%s.roleARN: %q is not an IAM role ARN such as arn:aws:iam::111122223333:role/sidekick-reader", key, a.RoleARN))
		case m[2] != a.ID:
			errs = append(errs, fmt.Errorf("%s.roleARN: the role is in account %s, not %s", key, m[2], a.ID))
		}

		if a.ExternalID != "" && !externalIDPattern.MatchString(a.ExternalID) {
			errs = append(errs, fmt.Errorf("%s.externalID: must be 2 to 1224 letters, digits or the characters _ + = , . @ : / -", key))
		}
	}

	return errs
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

// checkList checks that every value matches pattern, listed once.
func checkList(key string, values []string, pattern *regexp.Regexp, what string) []error {
	var errs []error

	seen := map[string]bool{}

	for i, v := range values {
		switch {
		case !pattern.MatchString(v):
			errs = append(errs, fmt.Errorf("%s[%d]: %q is not %s", key, i, source.CutText(v, maxLogGroupLen), what))
		case seen[v]:
			errs = append(errs, fmt.Errorf("%s[%d]: %q is listed twice", key, i, v))
		}

		seen[v] = true
	}

	return errs
}
