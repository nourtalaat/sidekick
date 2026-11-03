package gitlabmr

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const testBlobSHA = "ce013625030ba8dba906f756967f9e9ca394464a"

func TestReadBlob(t *testing.T) {
	t.Parallel()

	otherProject := validParams()
	otherProject.Project = "acme/secrets"

	tests := []struct {
		name    string
		params  Params
		blob    string
		answer  *fakehttp.Response
		want    string
		wantErr error
	}{
		{name: "the text of the blob", params: validParams(), blob: testBlobSHA, answer: ptr(reply(blobBody(testBlobSHA, "hello\n"))), want: "hello\n"},
		{name: "a project the config does not allow", params: otherProject, blob: testBlobSHA, wantErr: action.ErrLimit},
		{name: "a blob that is not a SHA", params: validParams(), blob: "../../files/x", wantErr: gitlabgit.ErrBadBlob},
		{name: "a blob the host does not have", params: validParams(), blob: testBlobSHA, answer: &notFound, wantErr: action.ErrPermanent},
		{name: "a binary file", params: validParams(), blob: testBlobSHA, answer: ptr(reply(blobBody(testBlobSHA, "a\x00b"))), wantErr: gitlabgit.ErrNotText},
		{name: "a file that is too large", params: validParams(), blob: testBlobSHA, answer: ptr(reply(blobBody(testBlobSHA, strings.Repeat("x", gitlabgit.MaxFileBytes+1)))), wantErr: gitlabgit.ErrTooBig},
		{
			name: "text that GitLab converted, so its size differs", params: validParams(), blob: testBlobSHA, wantErr: gitlabgit.ErrNotText,
			answer: ptr(reply(`{"size":99,"encoding":"base64","content":"aGVsbG8K","sha":"` + testBlobSHA + `"}`)),
		},
		{
			name: "content that is not base64", params: validParams(), blob: testBlobSHA, wantErr: gitlabgit.ErrNotBase64,
			answer: ptr(reply(`{"size":6,"encoding":"text","content":"hello\n","sha":"` + testBlobSHA + `"}`)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			if tt.answer != nil {
				b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+tt.blob), *tt.answer)
			}

			raw, err := json.Marshal(tt.params)
			require.NoError(t, err)

			got, err := b.mr.ReadBlob(t.Context(), raw, tt.blob)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, []string{"GET repository/blobs/" + testBlobSHA}, sent(b.gitlab), "reading a blob only reads")
		})
	}
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T {
	return &v
}
