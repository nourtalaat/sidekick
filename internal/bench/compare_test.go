package bench

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// update rewrites the golden files: go test ./internal/bench -update.
var update = flag.Bool("update", false, "rewrite the golden files in fixture/")

// Files of the comparison test.
const (
	compareA      = "fixture/compare-a.json"
	compareB      = "fixture/compare-b.json"
	compareGolden = "fixture/compare.golden"
)

func TestWriteComparison(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		a, b   string
		golden string
	}{
		{name: "two runs side by side", a: compareA, b: compareB, golden: compareGolden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, err := ReadReport(tt.a)
			require.NoError(t, err)

			b, err := ReadReport(tt.b)
			require.NoError(t, err)

			var out bytes.Buffer
			require.NoError(t, WriteComparison(&out, a, b))

			if *update {
				require.NoError(t, os.WriteFile(tt.golden, out.Bytes(), 0o600))
			}

			want, err := os.ReadFile(tt.golden)
			require.NoError(t, err, "run go test ./internal/bench -update to create the golden file")
			require.Equal(t, string(want), out.String())
		})
	}
}

func TestCompareFixturesMatchSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		file string
	}{
		{name: "the first report", file: compareA},
		{name: "the second report", file: compareB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := os.ReadFile(tt.file)
			require.NoError(t, err)

			var doc map[string]any
			require.NoError(t, json.Unmarshal(raw, &doc))
			require.NoError(t, reportSchema(t).Validate(doc))
		})
	}
}

func TestIntervalOverlaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b Interval
		want bool
	}{
		{name: "apart", a: Interval{0.1, 0.3}, b: Interval{0.4, 0.6}, want: false},
		{name: "touching", a: Interval{0.1, 0.4}, b: Interval{0.4, 0.6}, want: true},
		{name: "one inside the other", a: Interval{0, 1}, b: Interval{0.4, 0.6}, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.a.overlaps(tt.b))
			require.Equal(t, tt.want, tt.b.overlaps(tt.a))
		})
	}
}
