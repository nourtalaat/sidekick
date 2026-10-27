package audit_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
)

func TestWriteRejectsBadEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		entry   audit.Entry
		wantErr error
	}{
		{
			name:    "no actor kind",
			entry:   audit.Entry{Action: "route.updated"},
			wantErr: audit.ErrInvalidEntry,
		},
		{
			name:    "no action",
			entry:   audit.Entry{ActorKind: audit.ActorUser},
			wantErr: audit.ErrInvalidEntry,
		},
		{
			name:  "a value that is not JSON",
			entry: audit.Entry{ActorKind: audit.ActorUser, Action: "route.updated", New: make(chan int)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// A nil DBTX panics if Write reaches the database, which it
			// must not do for these entries.
			err := audit.NewWriter().Write(t.Context(), nil, tt.entry)
			require.Error(t, err)

			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
			}
		})
	}
}
