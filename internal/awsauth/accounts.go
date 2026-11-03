package awsauth

import (
	"errors"
	"fmt"
	"regexp"

	"github.com/nourtalaat/sidekick/internal/source"
)

// The shortest and longest external ID STS accepts.
const (
	minExternalIDLen = 2
	maxExternalIDLen = 1224
)

// maxShownLen is the most bytes of a bad value an error message shows.
const maxShownLen = 512

var (
	// accountPattern is the shape of an AWS account ID.
	accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
	// regionPattern is the shape of an AWS Region code, such as eu-west-1
	// or us-gov-west-1.
	regionPattern = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	// roleARNPattern is the shape of an IAM role ARN. The group is the
	// role's account.
	roleARNPattern = regexp.MustCompile(`^arn:aws(-[a-z]+)*:iam::([0-9]{12}):role/[\w+=,.@/-]{1,512}$`)
	// externalIDPattern is the characters an external ID STS accepts may
	// hold.
	externalIDPattern = regexp.MustCompile(`^[\w+=,.@:/-]+$`)
)

// Account is one AWS account a plugin works in, as its config lists it.
type Account struct {
	// ID is the 12-digit account ID.
	ID string `yaml:"id"`
	// RoleARN is an IAM role in the account that Sidekick assumes for
	// every call there. Empty means Sidekick's own credentials, which must
	// belong to this account, unless the plugin requires a role.
	RoleARN string `yaml:"roleARN"`
	// ExternalID is the external ID the role's trust policy asks for, if
	// any. It needs RoleARN.
	ExternalID string `yaml:"externalID"`
}

// AccountOption changes how CheckAccounts checks the accounts.
type AccountOption func(*accountOptions)

type accountOptions struct {
	requireRole bool
}

// RequireRole makes roleARN required for every account, so the plugin never
// uses the credentials Sidekick itself has. Without it, at most one account
// may leave out roleARN, since Sidekick's own credentials belong to one
// account only.
func RequireRole() AccountOption {
	return func(o *accountOptions) { o.requireRole = true }
}

// CheckAccounts checks a plugin's list of accounts without credentials or
// network calls: at least one account, each ID and role, no ID listed twice,
// and no more than one account that uses Sidekick's own credentials. Each
// error starts with the key of its field, accounts[i].
func CheckAccounts(accounts []Account, opts ...AccountOption) []error {
	var o accountOptions
	for _, opt := range opts {
		opt(&o)
	}

	var errs []error

	if len(accounts) == 0 {
		errs = append(errs, errors.New("accounts: list at least one account"))
	}

	seen := map[string]bool{}
	ownCredentials := ""

	for i, a := range accounts {
		key := fmt.Sprintf("accounts[%d]", i)

		switch {
		case !accountPattern.MatchString(a.ID):
			errs = append(errs, fmt.Errorf("%s.id: %q is not a 12-digit AWS account ID; quote it in YAML", key, a.ID))
		case seen[a.ID]:
			errs = append(errs, fmt.Errorf("%s.id: %q is listed twice", key, a.ID))
		}

		seen[a.ID] = true

		if a.RoleARN == "" {
			errs = append(errs, checkNoRole(key, a, o, ownCredentials)...)

			if !o.requireRole {
				ownCredentials = a.ID
			}

			continue
		}

		errs = append(errs, checkRole(key, a)...)
	}

	return errs
}

// CheckRegions checks a plugin's list of Regions: at least one, each a Region
// code, none listed twice.
func CheckRegions(regions []string) []error {
	var errs []error

	if len(regions) == 0 {
		errs = append(errs, errors.New("regions: list at least one Region"))
	}

	return append(errs, CheckList("regions", regions, regionPattern, "an AWS Region code such as us-east-1")...)
}

// CheckList checks that every value matches pattern and that none is listed
// twice. what says what a value should be, for the error.
func CheckList(key string, values []string, pattern *regexp.Regexp, what string) []error {
	var errs []error

	seen := map[string]bool{}

	for i, v := range values {
		switch {
		case !pattern.MatchString(v):
			errs = append(errs, fmt.Errorf("%s[%d]: %q is not %s", key, i, source.CutText(v, maxShownLen), what))
		case seen[v]:
			errs = append(errs, fmt.Errorf("%s[%d]: %q is listed twice", key, i, v))
		}

		seen[v] = true
	}

	return errs
}

// checkNoRole returns the problems with an account that has no role.
// ownCredentials is the account that already uses Sidekick's own
// credentials, or "".
func checkNoRole(key string, a Account, o accountOptions, ownCredentials string) []error {
	switch {
	case o.requireRole:
		return []error{fmt.Errorf("%s.roleARN: required, so that this plugin has credentials of its own", key)}
	case a.ExternalID != "":
		errs := []error{fmt.Errorf("%s.externalID: needs roleARN", key)}

		return append(errs, ownCredentialsError(key, ownCredentials)...)
	default:
		return ownCredentialsError(key, ownCredentials)
	}
}

// ownCredentialsError is the error of a second account that would use
// Sidekick's own credentials.
func ownCredentialsError(key, ownCredentials string) []error {
	if ownCredentials == "" {
		return nil
	}

	return []error{fmt.Errorf("%s.roleARN: required, because account %s already uses Sidekick's own credentials", key, ownCredentials)}
}

// checkRole returns the problems with an account's role and external ID.
func checkRole(key string, a Account) []error {
	var errs []error

	m := roleARNPattern.FindStringSubmatch(a.RoleARN)

	switch {
	case m == nil:
		errs = append(errs, fmt.Errorf("%s.roleARN: %q is not an IAM role ARN such as arn:aws:iam::111122223333:role/sidekick-reader", key, a.RoleARN))
	case m[2] != a.ID:
		errs = append(errs, fmt.Errorf("%s.roleARN: the role is in account %s, not %s", key, m[2], a.ID))
	}

	if n := len(a.ExternalID); n > 0 && (n < minExternalIDLen || n > maxExternalIDLen || !externalIDPattern.MatchString(a.ExternalID)) {
		errs = append(errs, fmt.Errorf("%s.externalID: must be %d to %d letters, digits or the characters _ + = , . @ : / -",
			key, minExternalIDLen, maxExternalIDLen))
	}

	return errs
}
