package strategy

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/strategies"
)

// runtimeStrategy returns version n of a runtime strategy called name.
func runtimeStrategy(t *testing.T, name string, n int) Strategy {
	t.Helper()

	st, problems := ParseRuntime(fmt.Appendf(nil, "name: %s\ndescription: Saved at runtime.\n", name), n)
	require.Empty(t, problems)

	return st
}

func TestSetWithRuntime(t *testing.T) {
	t.Parallel()

	base, err := Load(strategies.FS, filepath.Join("fixture", "valid"), nil)
	require.NoError(t, err)

	set := base.WithRuntime([]Strategy{
		runtimeStrategy(t, GenericAlert, 2),
		runtimeStrategy(t, "checkout-latency", 1),
		runtimeStrategy(t, "only-runtime", 4),
	})

	tests := []struct {
		name          string
		wantOrigin    Origin
		wantVersion   string
		wantOverrides []Origin
	}{
		{name: GenericAlert, wantOrigin: Runtime, wantVersion: "runtime:2", wantOverrides: []Origin{BuiltIn}},
		{name: "checkout-latency", wantOrigin: File, wantOverrides: []Origin{Runtime}},
		{name: "only-runtime", wantOrigin: Runtime, wantVersion: "runtime:4"},
		{name: "latency-alert", wantOrigin: BuiltIn},
	}

	entries := map[string]Entry{}
	for _, e := range set.Entries() {
		entries[e.Strategy.Name] = e
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, ok := set.Get(tt.name)
			require.True(t, ok)
			require.Equal(t, tt.wantOrigin, st.Origin)

			if tt.wantVersion != "" {
				require.Equal(t, tt.wantVersion, st.Version)
			}

			require.Equal(t, tt.wantOverrides, entries[tt.name].Overrides)
		})
	}

	require.Len(t, set.Entries(), len(base.Names())+1)

	t.Run("the layers stay available", func(t *testing.T) {
		t.Parallel()

		shadowed, ok := set.Layer(Runtime, "checkout-latency")
		require.True(t, ok)
		require.Equal(t, "runtime:1", shadowed.Version)

		builtIn, ok := set.Layer(BuiltIn, GenericAlert)
		require.True(t, ok)
		require.Equal(t, BuiltIn, builtIn.Origin)

		_, ok = set.Layer(File, GenericAlert)
		require.False(t, ok)
	})

	t.Run("the set it was made from does not change", func(t *testing.T) {
		t.Parallel()

		st, _ := base.Get(GenericAlert)
		require.Equal(t, BuiltIn, st.Origin)

		_, ok := base.Get("only-runtime")
		require.False(t, ok)
	})
}

func TestSetVersion(t *testing.T) {
	t.Parallel()

	set, err := Load(strategies.FS, "", nil)
	require.NoError(t, err)

	current, _ := set.Get(GenericAlert)

	tests := []struct {
		name      string
		strategy  string
		version   string
		wantExact bool
		wantErr   error
	}{
		{name: "the version in effect", strategy: GenericAlert, version: current.Version, wantExact: true},
		{name: "a version that is gone", strategy: GenericAlert, version: "runtime:9"},
		{name: "an unknown strategy", strategy: "missing", version: current.Version, wantErr: ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, exact, err := set.Version(t.Context(), tt.strategy, tt.version)
			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, tt.wantExact, exact)

			if tt.wantErr == nil {
				require.Equal(t, current.Version, st.Version)
			}
		})
	}

	require.Same(t, set, set.Current())
}
