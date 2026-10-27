package validate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTerraform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		module     string
		dir        string
		wantErrors []string
		wantPath   string
		wantLine   bool
	}{
		{name: "Datadog provider", module: "fixture/terraform/good/datadog"},
		{name: "Grafana provider", module: "fixture/terraform/good/grafana"},
		{name: "SigNoz provider", module: "fixture/terraform/good/signoz"},
		{name: "module directory that uses a local module", module: "fixture/terraform/good/nested", dir: "monitors"},
		{
			name:       "argument the resource does not have",
			module:     "fixture/terraform/bad/unsupported-argument",
			wantErrors: []string{"Unsupported argument"},
			wantPath:   "main.tf",
			wantLine:   true,
		},
		{
			name:       "required argument left out",
			module:     "fixture/terraform/bad/missing-argument",
			wantErrors: []string{"Missing required argument"},
			wantPath:   "main.tf",
			wantLine:   true,
		},
		{
			name:       "version constraint no mirrored version meets",
			module:     "fixture/terraform/bad/version-not-in-mirror",
			wantErrors: []string{notInImage},
			wantPath:   ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			res, err := newService(t, tools(t)).Validate(t.Context(), Request{
				Format: FormatTerraform,
				Dir:    tt.dir,
				Files:  fixtureFiles(t, tt.module),
			})
			require.NoError(t, err)
			require.Equal(t, len(tt.wantErrors) == 0, res.OK, messages(res.Errors))
			require.Len(t, res.Errors, len(tt.wantErrors), messages(res.Errors))

			for i, want := range tt.wantErrors {
				require.Contains(t, res.Errors[i].Message, want)
				require.Equal(t, tt.wantPath, res.Errors[i].Path)

				if tt.wantLine {
					require.Positive(t, res.Errors[i].Line)
				}
			}
		})
	}
}

func TestTerraformLockFileFromTerraform(t *testing.T) {
	t.Parallel()

	opts := tools(t)

	versions, err := readMirror(opts.Mirror)
	require.NoError(t, err)

	datadog := versions["registry.opentofu.org/datadog/datadog"]
	require.NotEmpty(t, datadog, "the mirror has no Datadog provider")

	version := datadog[len(datadog)-1]
	zip := filepath.Join(opts.Mirror, "registry.opentofu.org", "datadog", "datadog",
		fmt.Sprintf("terraform-provider-datadog_%s_%s_%s.zip", version, runtime.GOOS, runtime.GOARCH))

	content, err := os.ReadFile(zip) //nolint:gosec // The test's own mirror.
	require.NoError(t, err)

	sum := sha256.Sum256(content)

	tests := []struct {
		name   string
		source string
	}{
		{name: "source on Terraform's registry", source: "registry.terraform.io/datadog/datadog"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			files := []File{
				{Path: "main.tf", Content: fmt.Sprintf(`terraform {
  required_providers {
    datadog = { source = %q, version = %q }
  }
}

resource "datadog_monitor" "m" {
  name    = "m"
  type    = "metric alert"
  message = "m"
  query   = "sum(last_5m):sum:checkout.errors{env:prod}.as_count() > 50"
}
`, tt.source, version)},
				{Path: lockFile, Content: fmt.Sprintf(`provider %q {
  version = %q
  hashes = [
    "zh:%s",
  ]
}
`, tt.source, version, hex.EncodeToString(sum[:]))},
			}

			res, err := newService(t, opts).Validate(t.Context(), Request{Format: FormatTerraform, Files: files})
			require.NoError(t, err)
			require.True(t, res.OK, messages(res.Errors))
		})
	}
}

func TestTerraformLockedVersionNotInMirror(t *testing.T) {
	t.Parallel()

	mirror := fakeMirror(t, map[string][]string{"registry.opentofu.org/datadog/datadog": {"4.25.0"}})

	tests := []struct {
		name  string
		files []File
		want  []string
	}{
		{
			name:  "lock file written by Terraform",
			files: fixtureFiles(t, "fixture/terraform/bad/locked-version-not-in-mirror"),
			want: []string{
				"registry.terraform.io/datadog/datadog 3.0.0: " + notInImage + ", which has 4.25.0.",
				"validator.allowDownloads",
			},
		},
		{
			name: "lock file written by OpenTofu, for a provider the image does not have",
			files: []File{
				{Path: "main.tf", Content: "terraform {}\n"},
				{Path: lockFile, Content: "provider \"registry.opentofu.org/hashicorp/aws\" {\n  version = \"6.0.0\"\n}\n"},
			},
			want: []string{"registry.opentofu.org/hashicorp/aws 6.0.0: " + notInImage + ", which has no version of it."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// tofu is never run: the lock file check comes first.
			s := newService(t, Options{Mirror: mirror, Tofu: filepath.Join(t.TempDir(), "no-tofu")})

			res, err := s.Validate(t.Context(), Request{Format: FormatTerraform, Files: tt.files})
			require.NoError(t, err)
			require.False(t, res.OK)
			require.Len(t, res.Errors, 1, messages(res.Errors))
			require.Equal(t, lockFile, res.Errors[0].Path)

			for _, want := range tt.want {
				require.Contains(t, res.Errors[0].Message, want)
			}
		})
	}
}

func TestCheckLockFile(t *testing.T) {
	t.Parallel()

	m := mirror{"registry.opentofu.org/datadog/datadog": {"4.25.0"}}
	locked := []File{{Path: "monitors/" + lockFile, Content: "provider \"registry.terraform.io/DataDog/datadog\" {\n  version = \"4.25.0\"\n}\n"}}
	old := []File{{Path: lockFile, Content: "provider \"registry.opentofu.org/datadog/datadog\" {\n  version = \"3.0.0\"\n}\n"}}

	tests := []struct {
		name           string
		files          []File
		dir            string
		allowDownloads bool
		wantMissing    bool
	}{
		{name: "locked version in the mirror, under Terraform's host and in upper case", files: locked, dir: "monitors"},
		{name: "no lock file", files: []File{{Path: "main.tf"}}, dir: "."},
		{name: "lock file outside the module directory is not the module's", files: old, dir: "monitors"},
		{name: "locked version missing", files: old, dir: ".", wantMissing: true},
		{name: "locked version missing, downloads allowed", files: old, dir: ".", allowDownloads: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := tofuChecker{mirror: m, allowDownloads: tt.allowDownloads}

			_, missing := c.checkLockFile(tt.files, tt.dir)
			require.Equal(t, tt.wantMissing, missing)
		})
	}
}

func TestReadMirror(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		packages map[string][]string
		other    bool
		want     mirror
	}{
		{
			name: "providers on both hosts and versions out of order",
			packages: map[string][]string{
				"registry.opentofu.org/datadog/datadog": {"4.25.0", "4.24.0"},
				"registry.terraform.io/datadog/datadog": {"4.25.0"},
				"registry.opentofu.org/grafana/grafana": {"4.49.0"},
			},
			want: mirror{
				"registry.opentofu.org/datadog/datadog": {"4.24.0", "4.25.0"},
				"registry.opentofu.org/grafana/grafana": {"4.49.0"},
			},
		},
		{
			name:     "packages for another platform only",
			packages: map[string][]string{"registry.opentofu.org/datadog/datadog": {"4.25.0"}},
			other:    true,
			want:     mirror{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var dir string
			if tt.other {
				dir = fakeMirrorFor(t, tt.packages, "plan9_386")
			} else {
				dir = fakeMirror(t, tt.packages)
			}

			got, err := readMirror(dir)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParseLockFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    []lockedProvider
	}{
		{
			name: "two providers with hashes",
			content: `# This file is maintained automatically by "tofu init".
provider "registry.opentofu.org/datadog/datadog" {
  version     = "4.25.0"
  constraints = "~> 4.0"
  hashes = [
    "h1:abc=",
  ]
}

provider "registry.opentofu.org/grafana/grafana" {
  version = "4.49.0"
}
`,
			want: []lockedProvider{
				{address: "registry.opentofu.org/datadog/datadog", version: "4.25.0"},
				{address: "registry.opentofu.org/grafana/grafana", version: "4.49.0"},
			},
		},
		{name: "empty", content: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, parseLockFile(tt.content))
		})
	}
}

// fakeMirror makes a mirror of empty provider packages for this platform.
func fakeMirror(t *testing.T, packages map[string][]string) string {
	t.Helper()

	return fakeMirrorFor(t, packages, runtime.GOOS+"_"+runtime.GOARCH)
}

// fakeMirrorFor makes a mirror of empty provider packages for platform.
func fakeMirrorFor(t *testing.T, packages map[string][]string, platform string) string {
	t.Helper()

	dir := t.TempDir()

	for address, versions := range packages {
		providerDir := filepath.Join(dir, filepath.FromSlash(address))
		require.NoError(t, os.MkdirAll(providerDir, 0o700))

		for _, v := range versions {
			name := fmt.Sprintf("terraform-provider-%s_%s_%s.zip", filepath.Base(address), v, platform)
			require.NoError(t, os.WriteFile(filepath.Join(providerDir, name), nil, 0o600))
		}
	}

	return dir
}
