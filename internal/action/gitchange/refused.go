package gitchange

import (
	"fmt"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
)

// refusedGroup is a set of built-in refused globs and the reason they are
// refused, which the model reads in the error.
type refusedGroup struct {
	why   string
	globs []string
}

// builtinRefused are the paths no code change or revert may touch, whatever
// the operator allows. An operator can add to the list but not remove from
// it. A glob here matches in the same way as one in limits.paths, except that
// both the glob and the path are compared in lower case.
var builtinRefused = []refusedGroup{
	{why: "CI and build automation", globs: []string{
		".github/**",
		".gitlab/**",
		".circleci/**",
		".buildkite/**",
		".tekton/**",
		".gitea/**",
		".forgejo/**",
		".woodpecker/**",
		".semaphore/**",
		".azure-pipelines/**",
		".drone.yml",
		".travis.yml",
		".woodpecker.yml",
		".appveyor.yml",
		".cirrus.yml",
		"appveyor.yml",
		"bitbucket-pipelines.yml",
		"buildspec.yml",
		"**/.gitlab-ci.yml",
		"**/Jenkinsfile*",
		"**/azure-pipelines*.yml",
		"**/cloudbuild.y*ml",
	}},
	{why: "ownership and review rules", globs: []string{
		"CODEOWNERS",
		"**/CODEOWNERS",
		"renovate.json*",
		".renovaterc*",
	}},
	{why: "Git settings and hooks", globs: []string{
		".gitmodules",
		"**/.gitattributes",
		".husky/**",
		".githooks/**",
		".pre-commit-config.yaml",
		"lefthook.y*ml",
	}},
	{why: "secrets and keys", globs: []string{
		"**/.env",
		"**/.env.*",
		"**/*.env",
		"**/.envrc",
		"**/*.pem",
		"**/*.key",
		"**/*.p12",
		"**/*.pfx",
		"**/*.p8",
		"**/*.jks",
		"**/*.keystore",
		"**/id_rsa*",
		"**/id_ed25519*",
		"**/id_ecdsa*",
		"**/id_dsa*",
		"**/.npmrc",
		"**/.pypirc",
		"**/.netrc",
		"**/.git-credentials",
		"**/.htpasswd",
		"**/.aws/credentials",
		"**/.docker/config.json",
		"**/.kube/config",
		"**/*.tfvars",
		"**/*.tfvars.json",
		"**/*.tfstate",
		"**/*.tfstate.*",
		"**/secrets/**",
	}},
}

// CheckRefusedGlobs checks the globs an operator adds to the refused paths.
// Each error starts with the key of its field, limits.refusedPaths[i].
func CheckRefusedGlobs(extra []string) []error {
	var errs []error

	for i, glob := range extra {
		if err := CheckGlob(glob); err != nil {
			errs = append(errs, fmt.Errorf("limits.refusedPaths[%d]: %w", i, err))
		}
	}

	return errs
}

// CheckNotRefused returns an error that wraps action.ErrLimit and names the
// glob when p matches a built-in refused path or one of extra. Paths are
// compared in lower case, so a different case does not get past the list.
func CheckNotRefused(p string, extra []string) error {
	if match, ok := refusedBy(p, extra); ok {
		return fmt.Errorf("path %q is refused: it matches %s: %w", p, match, action.ErrLimit)
	}

	return nil
}

// CheckCodePath checks a path a code change wants to touch: it must match
// none of the refused paths, built in or in extra, even when a glob of
// allowed matches it, and it must match one glob of allowed. The error wraps
// action.ErrLimit.
func CheckCodePath(p string, allowed, extra []string) error {
	if match, ok := refusedBy(p, extra); ok {
		return fmt.Errorf("path %q is refused: it matches %s: %w", p, match, action.ErrLimit)
	}

	for _, glob := range allowed {
		if matchGlob(glob, p) {
			return nil
		}
	}

	return fmt.Errorf("path %q matches none of the limits.paths globs: %w", p, action.ErrLimit)
}

// refusedBy reports whether p matches a refused glob and describes the glob
// for the error.
func refusedBy(p string, extra []string) (match string, ok bool) {
	lower := strings.ToLower(p)

	for _, group := range builtinRefused {
		for _, glob := range group.globs {
			if matchGlob(strings.ToLower(glob), lower) {
				return fmt.Sprintf("the built-in refused path %q (%s)", glob, group.why), true
			}
		}
	}

	for _, glob := range extra {
		if matchGlob(strings.ToLower(glob), lower) {
			return fmt.Sprintf("limits.refusedPaths %q", glob), true
		}
	}

	return "", false
}
