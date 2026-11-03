package githubcode

import (
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/githubapp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Test values.
const (
	testKeyBits    = 2048
	testRepo       = "acme/app"
	testProposalID = "0192d3a4-0000-7000-8000-0000000000a1"
	testUndoID     = "0192d3a4-0000-7000-8000-0000000000a2"
	testInvestID   = "0192d3a4-0000-7000-8000-0000000000c1"
	testBaseSHA    = "aaaa000000000000000000000000000000000001"
	testEarlierSHA = "aaaa000000000000000000000000000000000009"
	testCommitSHA  = "cccc000000000000000000000000000000000003"
	testTreeSHA    = "dddd000000000000000000000000000000000004"
	testGoPath     = "internal/billing/invoice.go"
	testGoBefore   = "package billing\n\nfunc Total() int {\n\treturn 1\n}\n"
	testGoAfter    = "package billing\n\nfunc Total() int {\n\treturn 2\n}\n"
)

// testBranch is the branch the tests expect the action to make.
var testBranch = "sidekick/" + testProposalID

// testApp is a whole GitHub App for tests.
var testApp = githubapp.App{AppID: 11, InstallationID: 22, PrivateKeySecret: registry.SecretRef{Name: "code-change", Key: "key.pem"}}

// validLimits returns limits that pass the config check.
func validLimits() Limits {
	return Limits{
		Repos:  []string{testRepo},
		Paths:  []string{"internal/**", "cmd/**", "scripts/**"},
		Bounds: gitchange.Bounds{MaxFiles: 3, MaxChangedLines: 40, BaseBranch: "main"},
	}
}

// validParams returns parameters that pass the limits of validLimits and
// change the Go file.
func validParams() Params {
	return Params{
		Repo: testRepo,
		Edit: gitchange.Edit{
			BaseBranch: "main",
			Title:      "Fix the invoice total",
			Body:       "The total ignored the second line.",
			Files:      []gitchange.FileChange{{Path: testGoPath, Content: new(testGoAfter)}},
		},
	}
}
