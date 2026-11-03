package gitchange

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// refusedExamples has a path for every built-in refused glob, and every
// glob is named, so a new entry needs a new row.
var refusedExamples = []struct {
	glob string
	path string
}{
	{".github/**", ".github/workflows/ci.yml"},
	{".gitlab/**", ".gitlab/merge_request_templates/default.md"},
	{".circleci/**", ".circleci/config.yml"},
	{".buildkite/**", ".buildkite/pipeline.yml"},
	{".tekton/**", ".tekton/pipeline.yaml"},
	{".gitea/**", ".gitea/workflows/ci.yml"},
	{".forgejo/**", ".forgejo/workflows/ci.yml"},
	{".woodpecker/**", ".woodpecker/build.yml"},
	{".semaphore/**", ".semaphore/semaphore.yml"},
	{".azure-pipelines/**", ".azure-pipelines/ci.yml"},
	{".drone.yml", ".drone.yml"},
	{".travis.yml", ".travis.yml"},
	{".woodpecker.yml", ".woodpecker.yml"},
	{".appveyor.yml", ".appveyor.yml"},
	{".cirrus.yml", ".cirrus.yml"},
	{"appveyor.yml", "appveyor.yml"},
	{"bitbucket-pipelines.yml", "bitbucket-pipelines.yml"},
	{"buildspec.yml", "buildspec.yml"},
	{"**/.gitlab-ci.yml", "services/api/.gitlab-ci.yml"},
	{"**/Jenkinsfile*", "services/api/Jenkinsfile.release"},
	{"**/azure-pipelines*.yml", "services/api/azure-pipelines-ci.yml"},
	{"**/cloudbuild.y*ml", "services/api/cloudbuild.yaml"},
	{"CODEOWNERS", "CODEOWNERS"},
	{"**/CODEOWNERS", "docs/CODEOWNERS"},
	{"renovate.json*", "renovate.json5"},
	{".renovaterc*", ".renovaterc.json"},
	{".gitmodules", ".gitmodules"},
	{"**/.gitattributes", "services/.gitattributes"},
	{".husky/**", ".husky/pre-commit"},
	{".githooks/**", ".githooks/pre-push"},
	{".pre-commit-config.yaml", ".pre-commit-config.yaml"},
	{"lefthook.y*ml", "lefthook.yml"},
	{"**/.env", "services/api/.env"},
	{"**/.env.*", "services/api/.env.production"},
	{"**/*.env", "services/api/prod.env"},
	{"**/.envrc", "services/api/.envrc"},
	{"**/*.pem", "certs/server.pem"},
	{"**/*.key", "certs/server.key"},
	{"**/*.p12", "certs/client.p12"},
	{"**/*.pfx", "certs/client.pfx"},
	{"**/*.p8", "certs/AuthKey.p8"},
	{"**/*.jks", "certs/store.jks"},
	{"**/*.keystore", "app/release.keystore"},
	{"**/id_rsa*", "deploy/id_rsa.pub"},
	{"**/id_ed25519*", "deploy/id_ed25519"},
	{"**/id_ecdsa*", "deploy/id_ecdsa"},
	{"**/id_dsa*", "deploy/id_dsa"},
	{"**/.npmrc", "web/.npmrc"},
	{"**/.pypirc", "tools/.pypirc"},
	{"**/.netrc", "tools/.netrc"},
	{"**/.git-credentials", "tools/.git-credentials"},
	{"**/.htpasswd", "web/.htpasswd"},
	{"**/.aws/credentials", "tools/.aws/credentials"},
	{"**/.docker/config.json", "tools/.docker/config.json"},
	{"**/.kube/config", "tools/.kube/config"},
	{"**/*.tfvars", "infra/prod.tfvars"},
	{"**/*.tfvars.json", "infra/prod.tfvars.json"},
	{"**/*.tfstate", "infra/terraform.tfstate"},
	{"**/*.tfstate.*", "infra/terraform.tfstate.backup"},
	{"**/secrets/**", "services/api/secrets/db.yaml"},
}

func TestBuiltinRefusedPaths(t *testing.T) {
	t.Parallel()

	listed := map[string]bool{}

	for _, group := range builtinRefused {
		for _, glob := range group.globs {
			require.NoError(t, CheckGlob(glob), glob)
			require.False(t, listed[glob], "%s is listed twice", glob)

			listed[glob] = true
		}
	}

	covered := map[string]bool{}

	for _, ex := range refusedExamples {
		covered[ex.glob] = true
	}

	require.Equal(t, listed, covered, "every built-in glob has an example, and every example is a built-in glob")

	for _, ex := range refusedExamples {
		t.Run(ex.glob, func(t *testing.T) {
			t.Parallel()

			err := CheckNotRefused(ex.path, nil)
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, fmt.Sprintf("%q", ex.glob), "the error names the glob")
			require.ErrorContains(t, err, ex.path)
		})
	}
}

func TestCheckNotRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		extra   []string
		wantErr string
	}{
		{name: "a source file", path: "src/main.go"},
		{name: "a word that holds a refused name", path: "docs/envelope.md"},
		{name: "a directory named like a host", path: "src/github/client.go"},
		{name: "secrets in a longer word", path: "internal/secretsmanager/client.go"},
		{name: "a CI directory below the root is not read by the host", path: "services/.github/ci.yml"},
		{name: "build files are not refused", path: "Makefile"},
		{name: "a Dockerfile is not refused", path: "Dockerfile"},
		{name: "a package file is not refused", path: "package.json"},
		{name: "upper case does not get past the list", path: ".GitHub/workflows/ci.yml", wantErr: "built-in refused path"},
		{name: "a key in upper case", path: "certs/SERVER.PEM", wantErr: "built-in refused path"},
		{name: "an operator glob", path: "db/migrations/001.sql", extra: []string{"db/migrations/**"}, wantErr: `limits.refusedPaths "db/migrations/**"`},
		{name: "an operator glob in other case", path: "DB/Migrations/001.sql", extra: []string{"db/migrations/**"}, wantErr: `limits.refusedPaths "db/migrations/**"`},
		{name: "an operator glob that does not match", path: "db/seed.sql", extra: []string{"db/migrations/**"}},
		{name: "the built-in list still applies with an operator glob", path: ".github/ci.yml", extra: []string{"db/**"}, wantErr: "built-in refused path"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckNotRefused(tt.path, tt.extra)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCheckCodePath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		allowed []string
		extra   []string
		wantErr string
	}{
		{name: "an allowed path", path: "src/main.go", allowed: []string{"src/**"}},
		{name: "a path no glob allows", path: "docs/a.md", allowed: []string{"src/**"}, wantErr: "matches none of the limits.paths globs"},
		{name: "a CI file that a glob allows", path: ".github/workflows/ci.yml", allowed: []string{"**"}, wantErr: "built-in refused path"},
		{name: "a CODEOWNERS file that a glob allows", path: "CODEOWNERS", allowed: []string{"**"}, wantErr: "built-in refused path"},
		{name: "an env file that a glob allows", path: "src/.env", allowed: []string{"src/**"}, wantErr: "built-in refused path"},
		{name: "a path an operator refuses", path: "src/gen/a.go", allowed: []string{"src/**"}, extra: []string{"src/gen/**"}, wantErr: "limits.refusedPaths"},
		{name: "no allowed globs", path: "src/main.go", wantErr: "matches none of the limits.paths globs"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckCodePath(tt.path, tt.allowed, tt.extra)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestCheckRefusedGlobs(t *testing.T) {
	t.Parallel()

	require.Empty(t, CheckRefusedGlobs(nil))
	require.Empty(t, CheckRefusedGlobs([]string{"db/**", "**/*.sql"}))

	errs := CheckRefusedGlobs([]string{"db/**", "", "/abs", "a**b/c"})
	require.Len(t, errs, 3)
	require.ErrorContains(t, errs[0], "limits.refusedPaths[1]: required")
	require.ErrorContains(t, errs[1], "limits.refusedPaths[2]")
	require.ErrorContains(t, errs[2], "limits.refusedPaths[3]")
}
