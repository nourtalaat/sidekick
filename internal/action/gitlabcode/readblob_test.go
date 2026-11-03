package gitlabcode

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

func TestReadBlob(t *testing.T) {
	t.Parallel()

	blob := blobOf("hello\n")

	other := validParams()
	other.Project = "acme/secrets"

	tests := []struct {
		name    string
		params  Params
		blob    string
		answer  *fakehttp.Response
		want    string
		wantErr error
	}{
		{name: "the text of the blob", params: validParams(), blob: blob, answer: replyPtr(blobBody(blob, "hello\n")), want: "hello\n"},
		{name: "a project the config does not allow", params: other, blob: blob, wantErr: action.ErrLimit},
		{name: "a blob that is not a SHA", params: validParams(), blob: "../../files/x", wantErr: gitlabgit.ErrBadBlob},
		{name: "a blob the host does not have", params: validParams(), blob: blob, answer: &notFound, wantErr: action.ErrPermanent},
		{name: "a binary file", params: validParams(), blob: blob, answer: replyPtr(blobBody(blob, "a\x00b")), wantErr: gitlabgit.ErrNotText},
		{name: "a file that is too large", params: validParams(), blob: blob, answer: replyPtr(blobBody(blob, strings.Repeat("x", gitlabgit.MaxFileBytes+1))), wantErr: gitlabgit.ErrTooBig},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			if tt.answer != nil {
				b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+tt.blob), *tt.answer)
			}

			got, err := b.code.ReadBlob(t.Context(), rawParams(t, tt.params), tt.blob)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, []string{"GET repository/blobs/" + blob}, sent(b.gitlab), "reading a blob only reads")
		})
	}
}

// replyPtr returns a pointer to a JSON answer with the body.
func replyPtr(body string) *fakehttp.Response {
	r := reply(body)

	return &r
}
