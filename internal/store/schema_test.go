package store_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
)

// testSchemaMaxConns is the pool size the schema name tests ask for.
const testSchemaMaxConns = 2

func TestSchemaLockKey(t *testing.T) {
	t.Parallel()

	const key int64 = 0x736b2d7265617072

	tests := []struct {
		name   string
		schema string
		// same is the schema whose key must equal this one's, and other a
		// schema whose key must differ; empty other means the default.
		same  string
		other string
	}{
		{name: "the default schema keeps the key", schema: "", same: "", other: "sidekick_bench_a"},
		{name: "a schema gets its own key", schema: "sidekick_bench_a", same: "sidekick_bench_a", other: ""},
		{name: "two schemas get different keys", schema: "sidekick_bench_a", same: "sidekick_bench_a", other: "sidekick_bench_b"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := store.SchemaLockKey(key, tt.schema)
			require.Equal(t, store.SchemaLockKey(key, tt.same), got, "the key is the same every time")
			require.NotEqual(t, store.SchemaLockKey(key, tt.other), got)

			if tt.schema == "" {
				require.Equal(t, key, got)
			} else {
				require.NotEqual(t, store.SchemaLockKey(key+1, tt.schema), got, "the key depends on the base key")
			}
		})
	}
}

func TestSchemaNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema string
	}{
		{name: "upper case", schema: "Bench"},
		{name: "a quote", schema: `bench"; DROP SCHEMA public; --`},
		{name: "a leading digit", schema: "1bench"},
		{name: "empty", schema: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := store.OpenSchema(t.Context(), "postgres://localhost/unused", tt.schema, testSchemaMaxConns)
			require.ErrorIs(t, err, store.ErrBadSchemaName)
			require.ErrorIs(t, store.CreateSchema(t.Context(), nil, tt.schema), store.ErrBadSchemaName)
			require.ErrorIs(t, store.DropSchema(t.Context(), nil, tt.schema), store.ErrBadSchemaName)
		})
	}
}
