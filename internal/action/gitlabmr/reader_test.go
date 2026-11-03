package gitlabmr

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestReaderFile(t *testing.T) {
	t.Parallel()

	t.Run("a file is read once and pinned to the commit", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectFile(testRulesPath, testRulesBefore)

		r := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA)

		for range 2 {
			f, err := r.File(t.Context(), testRulesPath)
			require.NoError(t, err)
			require.Equal(t, gitlabgit.ReadFile{Text: testRulesBefore, SHA: gitchange.BlobSHA(testRulesBefore), Exists: true}, f)
		}

		require.Equal(t, []string{"GET repository/files/" + testRulesPath}, sent(b.gitlab))
		require.Equal(t, testBaseSHA, queryOf(t, b.gitlab, http.MethodGet, apiPath("repository/files/"+testRulesPath)).Get("ref"))
	})

	t.Run("a path with a space and a slash reaches GitLab escaped", func(t *testing.T) {
		t.Parallel()

		raw := make(chan string, 1)

		b := newBed(t, nil)
		b.gitlab.HandleFunc(http.MethodGet, apiPath("repository/files/prometheus/my rules.yaml"), func(w http.ResponseWriter, r *http.Request) {
			raw <- r.URL.EscapedPath()

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(fileBody("prometheus/my rules.yaml", "a\n")))
		})

		_, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).File(t.Context(), "prometheus/my rules.yaml")
		require.NoError(t, err)
		require.Equal(t, "/api/v4/projects/acme%2Fplatform%2Fmonitors/repository/files/prometheus%2Fmy%20rules.yaml", <-raw)
	})

	t.Run("a file that does not exist", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectNoFile("prometheus/rules/new.yaml", treeEntry(testRulesPath, gitlabgit.EntryBlob, "100644"))

		r := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA)

		f, err := r.File(t.Context(), "prometheus/rules/new.yaml")
		require.NoError(t, err)
		require.Equal(t, gitlabgit.ReadFile{}, f)
		require.Equal(t, "prometheus/rules", queryOf(t, b.gitlab, http.MethodGet, apiPath("repository/tree")).Get("path"))
	})

	t.Run("a file in a directory that does not exist", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/files/new/a.yaml"), notFound)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/tree"), notFound)

		f, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).File(t.Context(), "new/a.yaml")
		require.NoError(t, err)
		require.False(t, f.Exists)
	})

	t.Run("a file in the root has no path in the listing query", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectNoFile("a.yaml")

		_, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).File(t.Context(), "a.yaml")
		require.NoError(t, err)
		require.False(t, queryOf(t, b.gitlab, http.MethodGet, apiPath("repository/tree")).Has("path"))
	})

	t.Run("a name that is a directory", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.expectNoFile("prometheus/rules", treeEntry("prometheus/rules", "tree", "040000"))

		_, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).File(t.Context(), "prometheus/rules")
		require.ErrorIs(t, err, gitlabgit.ErrNotFile)
	})

	t.Run("a file that is too large", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/files/big.yaml"), reply(fileBody("big.yaml", strings.Repeat("x", gitlabgit.MaxFileBytes+1))))

		_, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).File(t.Context(), "big.yaml")
		require.ErrorIs(t, err, gitlabgit.ErrTooBig)
	})

	t.Run("an answer that is far too large", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/files/big.yaml"), reply(`{"content":"`+strings.Repeat("A", 2*gitlabgit.MaxFileBytes)+`"}`))

		_, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).File(t.Context(), "big.yaml")
		require.ErrorIs(t, err, gitlabgit.ErrTooBig)
	})

	t.Run("a file that is not text", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/files/a.bin"), reply(fileBody("a.bin", "a\x00b")))

		_, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).File(t.Context(), "a.bin")
		require.ErrorIs(t, err, gitlabgit.ErrNotText)
	})

	t.Run("a blob ID of a SHA-256 repository", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/files/a.yaml"), reply(fileBodyWithSHA("a.yaml", "a\n", strings.Repeat("a", 64))))

		_, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).File(t.Context(), "a.yaml")
		require.ErrorIs(t, err, gitlabgit.ErrUnsupportedHash)
	})
}

func TestReaderDir(t *testing.T) {
	t.Parallel()

	t.Run("the files of a directory, over two pages", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/tree"),
			fakehttp.Response{
				Headers: map[string]string{"Content-Type": "application/json", "X-Next-Page": "2"},
				Body:    []byte("[" + treeEntry("datadog/prod/main.tf", gitlabgit.EntryBlob, "100644") + "," + treeEntry("datadog/prod/modules", "tree", "040000") + "]"),
			},
			reply("["+treeEntry("datadog/prod/link.tf", gitlabgit.EntryBlob, gitlabgit.ModeSymlink)+","+treeEntry("datadog/prod/sub", "commit", "160000")+","+treeEntry("datadog/prod/vars.tf", gitlabgit.EntryBlob, "100755")+"]"),
		)
		b.gitlab.ExpectAllCalled()

		r := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA)

		got, err := r.Dir(t.Context(), "datadog/prod")
		require.NoError(t, err)
		require.Equal(t, []gitchange.DirEntry{
			{Name: "main.tf", Path: "datadog/prod/main.tf", SHA: gitchange.BlobSHA("datadog/prod/main.tf")},
			{Name: "vars.tf", Path: "datadog/prod/vars.tf", SHA: gitchange.BlobSHA("datadog/prod/vars.tf")},
		}, got, "only regular files are listed")

		queries := b.gitlab.Requests()
		require.Equal(t, "datadog/prod", queries[0].Query.Get("path"))
		require.Equal(t, testBaseSHA, queries[0].Query.Get("ref"))
		require.Equal(t, "100", queries[0].Query.Get("per_page"))
		require.Equal(t, "2", queries[1].Query.Get("page"))

		again, err := r.Dir(t.Context(), "datadog/prod")
		require.NoError(t, err)
		require.Equal(t, got, again)
		require.Len(t, b.gitlab.Requests(), 2, "a listing is read once")
	})

	t.Run("the root and a directory that does not exist", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("repository/tree"), reply("[]"), notFound)

		r := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA)

		root, err := r.Dir(t.Context(), ".")
		require.NoError(t, err)
		require.Empty(t, root)
		require.False(t, b.gitlab.Requests()[0].Query.Has("path"))

		missing, err := r.Dir(t.Context(), "nowhere")
		require.NoError(t, err)
		require.Empty(t, missing)
	})

	t.Run("a listing that never ends", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.HandleFunc(http.MethodGet, apiPath("repository/tree"), func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Next-Page", "99")
			_, _ = w.Write([]byte("[]"))
		})

		_, err := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA).Dir(t.Context(), "big")
		require.ErrorIs(t, err, gitlabgit.ErrTooManyEntries)
	})
}

func TestReaderBlob(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+testBlobSHA), reply(blobBody(testBlobSHA, "hello\n")))

	r := gitlabgit.NewReader(b.mr.client, testProject, testBaseSHA)
	entry := gitchange.DirEntry{Name: "a.tf", Path: "m/a.tf", SHA: testBlobSHA}

	for range 2 {
		text, err := r.Blob(t.Context(), entry)
		require.NoError(t, err)
		require.Equal(t, "hello\n", text)
	}

	require.Len(t, b.gitlab.Requests(), 1, "a blob is read once")
}
