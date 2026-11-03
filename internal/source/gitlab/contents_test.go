package gitlab

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestGetFileLimits(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("a", 300<<10)

	tests := []struct {
		name        string
		answer      string
		wantContent int
		wantOmitted string
	}{
		{
			name: "text over 256 KiB is cut",
			answer: fmt.Sprintf(`{"file_path":"x","size":%d,"encoding":"base64","content":%q,"blob_id":"b","commit_id":"c"}`,
				len(long), base64.StdEncoding.EncodeToString([]byte(long))),
			wantContent: maxFileSize,
		},
		{
			name:        "an answer too large to read leaves the file out",
			answer:      `{"file_path":"x","size":900000,"encoding":"base64","content":"` + strings.Repeat("A", maxFileBody) + `","blob_id":"b","commit_id":"c"}`,
			wantOmitted: omittedFileTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			api.Handle(http.MethodGet, filePath, body(tt.answer))
			api.ExpectAllCalled()

			s := newTestSource(t, api, 0)

			got, err := s.Call(t.Context(), source.Call{Tool: toolGetFile, Input: json.RawMessage(`{"project":"acme/checkout","path":"deploy/values.yaml"}`)})
			require.NoError(t, err)
			require.True(t, got.Truncated)

			var out fileResult

			require.NoError(t, json.Unmarshal(got.Structured, &out))
			require.Len(t, out.Content, tt.wantContent)
			require.Equal(t, tt.wantOmitted, out.Omitted)
		})
	}
}

func TestGetFileEncoding(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, filePath, body(`{"file_path":"x","size":3,"encoding":"text","content":"abc","blob_id":"b","commit_id":"c"}`))

	s := newTestSource(t, api, 0)

	_, err := s.Call(t.Context(), source.Call{Tool: toolGetFile, Input: json.RawMessage(`{"project":"acme/checkout","path":"deploy/values.yaml"}`)})
	require.ErrorContains(t, err, `GitLab sent the content as "text", not base64`)
}
