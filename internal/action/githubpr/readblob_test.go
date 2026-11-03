package githubpr

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const testBlobSHA = "ce013625030ba8dba906f756967f9e9ca394464a"

func TestReadBlob(t *testing.T) {
	t.Parallel()

	otherRepo := validParams()
	otherRepo.Repo = "acme/secrets"

	tests := []struct {
		name    string
		params  Params
		blob    string
		answer  *fakehttp.Response
		want    string
		wantErr error
	}{
		{name: "the text of the blob", params: validParams(), blob: testBlobSHA, answer: &fakehttp.Response{Body: []byte("hello\n")}, want: "hello\n"},
		{name: "a repository the config does not allow", params: otherRepo, blob: testBlobSHA, wantErr: action.ErrLimit},
		{name: "a blob that is not a SHA", params: validParams(), blob: "../../contents/x", wantErr: errBadBlob},
		{name: "a blob the host does not have", params: validParams(), blob: testBlobSHA, answer: &notFound, wantErr: action.ErrPermanent},
		{name: "a binary file", params: validParams(), blob: testBlobSHA, answer: &fakehttp.Response{Body: []byte("a\x00b")}, wantErr: errNotText},
		{name: "a file that is too large", params: validParams(), blob: testBlobSHA, answer: &fakehttp.Response{Body: []byte(strings.Repeat("x", maxBaseFileBytes+1))}, wantErr: errTooBig},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			if tt.answer != nil {
				b.github.Handle(http.MethodGet, repoPath("git/blobs/"+tt.blob), *tt.answer)
			}

			raw, err := json.Marshal(tt.params)
			require.NoError(t, err)

			got, err := b.pr.ReadBlob(t.Context(), raw, tt.blob)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}
