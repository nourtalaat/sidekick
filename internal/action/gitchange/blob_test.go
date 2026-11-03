package gitchange

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBlobSHA(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "a line of text", content: "hello\n", want: "ce013625030ba8dba906f756967f9e9ca394464a"},
		{name: "the empty blob", content: "", want: "e69de29bb2d1d6434b8b29ae775ad8c2e48c5391"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, BlobSHA(tt.content), "what git hash-object gives")
		})
	}
}
