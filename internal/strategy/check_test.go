package strategy

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const validBody = "name: checkout-latency\ndescription: Checkout is slow.\nfirst:\n  - Check what changed.\n"

func TestCheck(t *testing.T) {
	t.Parallel()

	const manyProblems = "name: Bad Name\ndescription: Checkout.\nmatch:\n  textPattern: \"(\"\nwatchers:\n  - name: w\n    check:\n      type: nope\n"

	tests := []struct {
		name string
		body string
		// want lists the path and line of each problem, in order.
		want []FieldProblem
	}{
		{name: "a valid strategy", body: validBody},
		{name: "an empty body", body: "", want: []FieldProblem{{Message: "the file is empty"}}},
		{name: "two documents", body: validBody + "---\nname: other\n", want: []FieldProblem{{Message: "the file must hold one YAML document"}}},
		{name: "an unknown key", body: validBody + "colour: red\n", want: []FieldProblem{{Message: `line 5: unknown key "colour"`}}},
		{
			name: "a missing key has no line",
			body: "name: checkout-latency\n",
			want: []FieldProblem{{Path: "description", Message: "required"}},
		},
		{
			name: "paths and lines",
			body: manyProblems,
			want: []FieldProblem{
				{Path: "name", Line: 1},
				{Path: "match.textPattern", Line: 4},
				{Path: "watchers[0].check.type", Line: 8},
			},
		},
		{name: "a body over the limit", body: validBody + strings.Repeat("#", MaxBodyBytes), want: []FieldProblem{{Message: "the strategy is larger than 64 KiB"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			st, got := Check([]byte(tt.body))
			require.Len(t, got, len(tt.want))

			for i, w := range tt.want {
				require.Equal(t, w.Path, got[i].Path)
				require.Equal(t, w.Line, got[i].Line, got[i].String())

				if w.Message != "" {
					require.Equal(t, w.Message, got[i].Message)
				} else {
					require.NotEmpty(t, got[i].Message)
				}
			}

			if len(tt.want) == 0 {
				require.Equal(t, "checkout-latency", st.Name)
				require.Equal(t, Runtime, st.Origin)
				require.Empty(t, st.Version)
				require.Equal(t, tt.body, st.Body)
			}
		})
	}
}

func TestParseRuntime(t *testing.T) {
	t.Parallel()

	st, problems := ParseRuntime([]byte(validBody), 3)
	require.Empty(t, problems)
	require.Equal(t, "runtime:3", st.Version)
	require.Equal(t, Runtime, st.Origin)

	_, problems = ParseRuntime([]byte("name: x\n"), 3)
	require.NotEmpty(t, problems)
}

func TestRuntimeVersionNumber(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version string
		want    int
		wantOK  bool
	}{
		{version: "runtime:1", want: 1, wantOK: true},
		{version: "runtime:42", want: 42, wantOK: true},
		{version: "runtime:0"},
		{version: "runtime:-2"},
		{version: "runtime:x"},
		{version: "runtime:"},
		{version: "0123456789abcdef"},
		{version: ""},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			t.Parallel()

			got, ok := RuntimeVersionNumber(tt.version)
			require.Equal(t, tt.wantOK, ok)
			require.Equal(t, tt.want, got)
		})
	}

	require.Equal(t, "runtime:7", RuntimeVersion(7))
}

func TestFieldProblemString(t *testing.T) {
	t.Parallel()

	require.Equal(t, "match.textPattern: bad", FieldProblem{Path: "match.textPattern", Message: "bad", Line: 4}.String())
	require.Equal(t, "the file is empty", FieldProblem{Message: "the file is empty"}.String())
}
