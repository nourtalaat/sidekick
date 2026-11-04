package gitlabcode

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

func TestReadFileAt(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("x", gitlabgit.MaxFileBytes+1)

	tests := []struct {
		name       string
		repo       repo
		path       string
		want       gitlabgit.ReadFile
		wantErr    string
		wantNoRead bool
	}{
		{name: "a regular file", repo: repo{testPath: regular(testBefore)}, path: testPath, want: file(testBefore)},
		{name: "an executable file", repo: repo{testPath: node{text: testBefore, mode: "100755"}}, path: testPath, want: file(testBefore)},
		{name: "a file in the root", repo: repo{"go.mod": regular("module x\n")}, path: "go.mod", want: file("module x\n")},
		{name: "a file that does not exist, in a directory that does", repo: repo{testPath: regular(testBefore)}, path: "src/billing/new.go", wantNoRead: true},
		{name: "a file in a directory that does not exist", repo: repo{testPath: regular(testBefore)}, path: "pkg/x/new.go", wantNoRead: true},
		{name: "a symlink", repo: repo{testPath: node{text: "target", mode: gitlabgit.ModeSymlink}}, path: testPath, wantErr: "is a symlink", wantNoRead: true},
		{name: "a submodule", repo: repo{"src/billing/vendor": node{kind: "commit", mode: "160000"}}, path: "src/billing/vendor", wantErr: "is a submodule", wantNoRead: true},
		{name: "a directory", repo: repo{testPath: regular(testBefore)}, path: "src/billing", wantErr: "is a directory", wantNoRead: true},
		{name: "a path through a file", repo: repo{testPath: regular(testBefore)}, path: testPath + "/x.go", wantErr: "goes through a path that is a file", wantNoRead: true},
		{name: "a name that differs only in case", repo: repo{"src/Billing/invoice.go": regular(testBefore)}, path: testPath, wantErr: "differs only in case", wantNoRead: true},
		{name: "a file that is too large", repo: repo{testPath: regular(big)}, path: testPath, wantErr: "is larger than"},
		{name: "a file that is not text", repo: repo{testPath: regular("a\x00b")}, path: testPath, wantErr: "is not a UTF-8 text file"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.serveRepo(tt.repo)

			r := gitlabgit.NewReader(b.code.client, testProject, testBaseSHA)

			got, err := readFileAt(t.Context(), r, tt.path)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit, "a refusal that the approver reads")
				require.ErrorIs(t, err, action.ErrPermanent, "and that a repeat does not change")
			} else {
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
			}

			if tt.wantNoRead {
				for _, call := range sent(b.gitlab) {
					require.NotContains(t, call, "repository/files/", "a path that is no regular file is not read")
				}
			}
		})
	}
}

func TestReadFileAtReadsEachListingOnce(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.serveRepo(repo{testPath: regular(testBefore), "src/billing/other.go": regular("package billing\n")})

	r := gitlabgit.NewReader(b.code.client, testProject, testBaseSHA)

	for _, p := range []string{testPath, "src/billing/other.go", "src/billing/missing.go"} {
		_, err := readFileAt(t.Context(), r, p)
		require.NoError(t, err)
	}

	listings := 0

	for _, call := range sent(b.gitlab) {
		if call == "GET repository/tree" {
			listings++
		}
	}

	require.Equal(t, 3, listings, "the root, src and src/billing, once each")
}

func TestReadFileAtRefusesAListingThatNeverEnds(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.gitlab.HandleFunc(http.MethodGet, apiPath("repository/tree"), func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Next-Page", "99")
		writeJSON(w, http.StatusOK, "[]")
	})

	_, err := readFileAt(t.Context(), gitlabgit.NewReader(b.code.client, testProject, testBaseSHA), testPath)
	require.ErrorIs(t, err, gitlabgit.ErrTooManyEntries)
	require.ErrorIs(t, err, action.ErrLimit)
}

func TestReadFileAtKeepsAServerErrorRetryable(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.gitlab.Handle(http.MethodGet, apiPath("repository/tree"), failed(http.StatusInternalServerError, "boom"))

	_, err := readFileAt(t.Context(), gitlabgit.NewReader(b.code.client, testProject, testBaseSHA), testPath)
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrLimit)
	require.NotErrorIs(t, err, action.ErrPermanent)
}

// file returns the file the reader gives for text.
func file(text string) gitlabgit.ReadFile {
	return gitlabgit.ReadFile{Text: text, SHA: blobOf(text), Exists: true}
}
