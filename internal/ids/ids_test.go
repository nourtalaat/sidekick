package ids_test

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/ids"
)

const idsInOrderCheck = 1000

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		check func(t *testing.T)
	}{
		{
			name: "is a version 7 RFC 9562 UUID",
			check: func(t *testing.T) {
				t.Helper()

				id := ids.New()
				require.Equal(t, uuid.Version(7), id.Version())
				require.Equal(t, uuid.RFC4122, id.Variant())
			},
		},
		{
			name: "IDs made one after another are unique and sort in order",
			check: func(t *testing.T) {
				t.Helper()

				seen := make(map[uuid.UUID]bool, idsInOrderCheck)
				prev := ids.New()

				for range idsInOrderCheck {
					id := ids.New()
					require.False(t, seen[id], "duplicate ID %s", id)
					require.Positive(t, bytes.Compare(id[:], prev[:]), "%s does not sort after %s", id, prev)

					seen[id] = true
					prev = id
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.check(t)
		})
	}
}
