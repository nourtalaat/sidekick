package strategystore

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/strategies"
)

func TestMerge(t *testing.T) {
	t.Parallel()

	base, err := strategy.Load(strategies.FS, "../strategy/fixture/valid", nil)
	require.NoError(t, err)

	const (
		generic  = "name: generic-alert\ndescription: Saved at runtime.\n"
		checkout = "name: checkout-latency\ndescription: Saved at runtime.\n"
		onlyRT   = "name: only-runtime\ndescription: Saved at runtime.\n"
		badRegex = "name: broken\ndescription: Saved at runtime.\nmatch:\n  textPattern: \"(\"\n"
		renamed  = "name: other-name\ndescription: Saved at runtime.\n"
	)

	got := Merge(base, []Version{
		{Name: "broken", Number: 2, Body: badRegex},
		{Name: "checkout-latency", Number: 1, Body: checkout},
		{Name: "generic-alert", Number: 3, Body: generic},
		{Name: "only-runtime", Number: 4, Body: onlyRT},
		{Name: "stored-as", Number: 1, Body: renamed},
	})

	tests := []struct {
		name        string
		wantOrigin  strategy.Origin
		wantVersion string
	}{
		{name: "generic-alert", wantOrigin: strategy.Runtime, wantVersion: "runtime:3"},
		{name: "only-runtime", wantOrigin: strategy.Runtime, wantVersion: "runtime:4"},
		{name: "checkout-latency", wantOrigin: strategy.File},
		{name: "latency-alert", wantOrigin: strategy.BuiltIn},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, ok := got.Set.Get(tt.name)
			require.True(t, ok)
			require.Equal(t, tt.wantOrigin, st.Origin)

			if tt.wantVersion != "" {
				require.Equal(t, tt.wantVersion, st.Version)
			}
		})
	}

	_, ok := got.Set.Get("broken")
	require.False(t, ok)

	require.Equal(t, []Skipped{{Name: "checkout-latency", Version: 1, Reason: reasonShadowed}}, got.Shadowed)

	require.Len(t, got.Invalid, 2)
	require.Equal(t, "broken", got.Invalid[0].Name)
	require.Contains(t, got.Invalid[0].Reason, "match.textPattern")
	require.Equal(t, "stored-as", got.Invalid[1].Name)
	require.Contains(t, got.Invalid[1].Reason, "name")
}

func TestMergeWithoutRuntimeStrategies(t *testing.T) {
	t.Parallel()

	base, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	got := Merge(base, nil)
	require.Equal(t, base.Names(), got.Set.Names())
	require.Empty(t, got.Shadowed)
	require.Empty(t, got.Invalid)
}

func TestCheck(t *testing.T) {
	t.Parallel()

	_, err := check(SaveInput{Name: "x", Body: "name: x\ndescription: d\n", Reason: "why"})
	require.NoError(t, err)

	_, err = check(SaveInput{Name: "y", Body: "name: x\ndescription: d\n", Reason: "why"})

	var invalid *InvalidError

	require.ErrorAs(t, err, &invalid)
	require.Empty(t, invalid.Reason)
	require.Equal(t, "name", invalid.Body[0].Path)
	require.Contains(t, invalid.Error(), `must be "y"`)
}
