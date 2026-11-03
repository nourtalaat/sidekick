package azure

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// later is how long after testNow the second call for armScope is.
		later     time.Duration
		wantCalls int
	}{
		{name: "a token is kept while it is fresh", later: 30 * time.Minute, wantCalls: 2},
		{name: "a token close to expiring is replaced", later: 56 * time.Minute, wantCalls: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cred := &fakeCredential{}
			tokens := newTokens(cred)
			now := testNow
			tokens.now = func() time.Time { return now }

			first, err := tokens.token(t.Context(), armScope)
			require.NoError(t, err)
			require.Equal(t, "token-for-"+armScope, first)

			other, err := tokens.token(t.Context(), logAnalyticsScope)
			require.NoError(t, err)
			require.Equal(t, "token-for-"+logAnalyticsScope, other, "each scope has its own token")

			now = testNow.Add(tt.later)

			again, err := tokens.token(t.Context(), armScope)
			require.NoError(t, err)
			require.Equal(t, first, again)
			require.Equal(t, tt.wantCalls, cred.calls)
		})
	}
}
