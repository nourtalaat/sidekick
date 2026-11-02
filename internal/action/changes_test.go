package action

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		before, after string
		want          []Change
		wantErr       error
	}{
		{name: "same", before: `{"a":1,"b":{"c":[1,2]}}`, after: `{"b":{"c":[1,2]},"a":1}`},
		{
			name: "nested value", before: `{"options":{"thresholds":{"critical":80,"warning":60}}}`,
			after: `{"options":{"thresholds":{"critical":90,"warning":60}}}`,
			want:  []Change{{Path: "options.thresholds.critical", Before: ptr("80"), After: ptr("90")}},
		},
		{
			name: "added and removed keys", before: `{"a":1,"b":2}`, after: `{"a":1,"c":3}`,
			want: []Change{{Path: "b", Before: ptr("2")}, {Path: "c", After: ptr("3")}},
		},
		{name: "null is not absent", before: `{"a":null}`, after: `{}`, want: []Change{{Path: "a", Before: ptr("null")}}},
		{name: "numbers by value", before: `{"a":90,"b":1.0}`, after: `{"a":90.0,"b":1}`},
		{name: "big whole numbers keep every digit", before: `{"a":9007199254740993}`, after: `{"a":9007199254740992}`, want: []Change{{Path: "a", Before: ptr("9007199254740993"), After: ptr("9007199254740992")}}},
		{
			name: "array of scalars is one change", before: `{"tags":["a","b"]}`, after: `{"tags":["b","a"]}`,
			want: []Change{{Path: "tags", Before: ptr(`["a","b"]`), After: ptr(`["b","a"]`)}},
		},
		{
			name: "array of objects by position", before: `{"rules":[{"expr":"x","for":"1m"},{"expr":"y"}]}`,
			after: `{"rules":[{"expr":"x","for":"5m"},{"expr":"y"}]}`,
			want:  []Change{{Path: "rules[0].for", Before: ptr(`"1m"`), After: ptr(`"5m"`)}},
		},
		{
			name: "arrays of objects of other lengths are whole", before: `{"rules":[{"a":1}]}`, after: `{"rules":[{"a":1},{"a":2}]}`,
			want: []Change{{Path: "rules", Before: ptr(`[{"a":1}]`), After: ptr(`[{"a":1},{"a":2}]`)}},
		},
		{name: "type change", before: `{"a":"1"}`, after: `{"a":1}`, want: []Change{{Path: "a", Before: ptr(`"1"`), After: ptr("1")}}},
		{name: "hidden characters are spelled out", before: `{"m":"a"}`, after: "{\"m\":\"a\u200bb\"}", want: []Change{{Path: "m", Before: ptr(`"a"`), After: ptr(`"a\u200bb"`)}}},
		{name: "removed definition", before: `{"a":1}`, after: `null`, want: []Change{{Path: "", Before: ptr(`{"a":1}`)}}},
		{name: "created definition", before: ``, after: `{"a":1}`, want: []Change{{Path: "", After: ptr(`{"a":1}`)}}},
		{name: "nothing on either side", before: `null`, after: ``},
		{name: "not JSON", before: `{`, after: `{}`, wantErr: ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Changes(Snapshot{Before: json.RawMessage(tt.before), After: json.RawMessage(tt.after)})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, nilToEmpty(got))
		})
	}
}

func TestDiffLimits(t *testing.T) {
	t.Parallel()

	wide := map[string]int{}
	other := map[string]int{}

	for i := range MaxChanges + 1 {
		wide["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = 1
		other["k"+string(rune('a'+i%26))+string(rune('a'+i/26))] = 2
	}

	before, err := json.Marshal(wide)
	require.NoError(t, err)

	after, err := json.Marshal(other)
	require.NoError(t, err)

	_, err = Diff(before, after)
	require.ErrorIs(t, err, ErrTooManyChanges)
	require.ErrorIs(t, err, ErrLimit)

	deep := func(leaf string) json.RawMessage {
		s := leaf
		for range maxDiffDepth + 2 {
			s = `{"x":` + s + `}`
		}

		return json.RawMessage(s)
	}

	_, err = Diff(deep("1"), deep("2"))
	require.ErrorIs(t, err, ErrTooManyChanges)
}

func TestSnapshotDeletes(t *testing.T) {
	t.Parallel()

	require.True(t, Snapshot{Before: json.RawMessage(`{"a":1}`), After: json.RawMessage(`null`)}.Deletes())
	require.False(t, Snapshot{Before: json.RawMessage(`{"a":1}`), After: json.RawMessage(`{}`)}.Deletes())
	require.False(t, Snapshot{}.Deletes())
}

func TestConflictError(t *testing.T) {
	t.Parallel()

	var err error = &ConflictError{Live: json.RawMessage(`{}`)}

	require.ErrorIs(t, err, ErrConflict)
	require.Equal(t, ErrConflict.Error(), err.Error())
}

func ptr(s string) *string { return &s }

func nilToEmpty(c []Change) []Change {
	if len(c) == 0 {
		return nil
	}

	return c
}
