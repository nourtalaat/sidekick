package gitlabrevert

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

func TestReadBlob(t *testing.T) {
	t.Parallel()

	blob := gitchange.BlobSHA(testBefore)

	tests := []struct {
		name    string
		blob    string
		change  func(p *Params)
		body    string
		status  int
		want    string
		wantErr error
	}{
		{name: "a blob of an allowed project", blob: blob, body: blobBody(testBefore), want: testBefore},
		{name: "a project that is not allowed", blob: blob, change: func(p *Params) { p.Project = "acme/other/project" }, wantErr: action.ErrLimit},
		{name: "something that is not a blob ID", blob: "main", wantErr: gitlabgit.ErrBadBlob},
		{name: "a blob GitLab does not have", blob: blob, status: http.StatusNotFound, body: `{"message":"404 Blob Not Found"}`, wantErr: action.ErrPermanent},
		{name: "a blob that is not text", blob: blob, body: blobBody("a\x00b"), wantErr: gitlabgit.ErrNotText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.w.srv.HandleFunc(http.MethodGet, apiPath("repository/blobs/"+blob), func(rw http.ResponseWriter, _ *http.Request) {
				status := tt.status
				if status == 0 {
					status = http.StatusOK
				}

				writeJSON(rw, status, tt.body)
			})

			p := commitParams()
			if tt.change != nil {
				tt.change(&p)
			}

			raw, err := json.Marshal(p)
			require.NoError(t, err)

			got, err := b.rv.ReadBlob(t.Context(), raw, tt.blob)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, []string{"GET repository/blobs/" + blob}, sent(b.w.srv), "reading a blob only reads")
		})
	}
}
