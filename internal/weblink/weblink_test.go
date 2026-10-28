package weblink

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvestigation(t *testing.T) {
	t.Parallel()

	const id = "0192d3a4-0000-7000-8000-0000000000c1"

	tests := []struct {
		name      string
		publicURL string
		id        string
		want      string
	}{
		{name: "https", publicURL: "https://sidekick.example.com", id: id, want: "https://sidekick.example.com/investigations/" + id},
		{name: "a trailing slash", publicURL: "https://sidekick.example.com/", id: id, want: "https://sidekick.example.com/investigations/" + id},
		{name: "a path prefix", publicURL: "https://example.com/sidekick/", id: id, want: "https://example.com/sidekick/investigations/" + id},
		{name: "http on a loopback address", publicURL: "http://127.0.0.1:8080", id: id, want: "http://127.0.0.1:8080/investigations/" + id},
		{name: "spaces around the URL", publicURL: "  https://sidekick.example.com  ", id: id, want: "https://sidekick.example.com/investigations/" + id},
		{name: "an ID with characters that need escaping", publicURL: "https://sidekick.example.com", id: "a/b c", want: "https://sidekick.example.com/investigations/a%2Fb%20c"},
		{name: "no public URL", publicURL: "", id: id, want: ""},
		{name: "a scheme that is not http", publicURL: "javascript:alert(1)", id: id, want: ""},
		{name: "a relative URL", publicURL: "/sidekick", id: id, want: ""},
		{name: "no host", publicURL: "https://", id: id, want: ""},
		{name: "no ID", publicURL: "https://sidekick.example.com", id: "", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, Investigation(tt.publicURL, tt.id))
		})
	}
}
