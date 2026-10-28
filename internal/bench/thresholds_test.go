package bench

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThresholdsCheck(t *testing.T) {
	t.Parallel()

	// summary has verdict accuracy 0.8, no false positive verdicts, a
	// root-cause match rate of 0.5, a mean cost of $0.30 a run and an
	// overall score of 0.7, with the interval 0.6 to 0.8.
	summary := Summary{
		Metrics: Metrics{
			VerdictAccuracy:   new(0.8),
			RootCauseMatch:    new(0.5),
			MeanCostPerRunUSD: new(0.3),
			Overall:           new(0.7),
		},
		Intervals: map[string]Interval{"overall": {Lower: 0.6, Upper: 0.8}},
	}

	baseline := func(overall float64, interval Interval) *JSONReport {
		return &JSONReport{Metrics: Metrics{Overall: new(overall)}, Intervals: map[string]Interval{"overall": interval}}
	}

	tests := []struct {
		name       string
		thresholds Thresholds
		baseline   *JSONReport
		want       []ThresholdResult
	}{
		{
			name: "nothing set checks nothing",
		},
		{
			name:       "met and failed absolute thresholds",
			thresholds: Thresholds{VerdictAccuracy: 0.8, RootCauseMatch: 0.7, MaxCostPerRunUSD: 0.25},
			want: []ThresholdResult{
				{Name: thresholdVerdictAccuracy, Limit: 0.8, Value: new(0.8), Passed: true},
				{Name: thresholdRootCauseMatch, Limit: 0.7, Value: new(0.5)},
				{Name: thresholdMaxCostPerRun, Limit: 0.25, Value: new(0.3)},
			},
		},
		{
			name:       "an empty class passes",
			thresholds: Thresholds{FPPrecision: 0.95},
			want:       []ThresholdResult{{Name: thresholdFPPrecision, Limit: 0.95, Passed: true, Detail: nothingToCount}},
		},
		{
			name:     "a drop within the default maxDrop",
			baseline: baseline(0.71, Interval{Lower: 0.6, Upper: 0.8}),
			want: []ThresholdResult{{
				Name: thresholdBaseline, Limit: 0.71 - DefaultMaxDrop, Value: new(0.7), Passed: true,
				Detail: "baseline 0.71, at most 0.02 lower; no clear difference",
			}},
		},
		{
			name:       "a drop beyond maxDrop",
			thresholds: Thresholds{MaxDrop: new(0.05)},
			baseline:   baseline(0.9, Interval{Lower: 0.85, Upper: 0.95}),
			want: []ThresholdResult{{
				Name: thresholdBaseline, Limit: 0.9 - 0.05, Value: new(0.7),
				Detail: "baseline 0.90, at most 0.05 lower; clear difference",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.thresholds.Check(summary, tt.baseline)
			require.Len(t, got, len(tt.want))

			for i, want := range tt.want {
				require.Equal(t, want.Name, got[i].Name)
				require.InDelta(t, want.Limit, got[i].Limit, shareDelta)
				requireShare(t, want.Value, got[i].Value)
				require.Equal(t, want.Passed, got[i].Passed)
				require.Equal(t, want.Detail, got[i].Detail)
			}

			require.Len(t, Failed(got), countFailed(tt.want))
		})
	}
}

func TestLoadSettingsThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		want    Thresholds
		wantErr string
	}{
		{
			name: "every threshold",
			file: "thresholds: { verdictAccuracy: 0.8, fpPrecision: 0.95, rootCauseMatch: 0.7, maxCostPerRunUSD: 0.5, baseline: bench/baseline.json, maxDrop: 0.02 }\n",
			want: Thresholds{VerdictAccuracy: 0.8, FPPrecision: 0.95, RootCauseMatch: 0.7, MaxCostPerRunUSD: 0.5, Baseline: "bench/baseline.json", MaxDrop: new(0.02)},
		},
		{
			name:    "a share above 1",
			file:    "thresholds: { verdictAccuracy: 80 }\n",
			wantErr: "thresholds.verdictAccuracy: must be from 0 to 1, not 80",
		},
		{
			name:    "a negative cost",
			file:    "thresholds: { maxCostPerRunUSD: -1 }\n",
			wantErr: "thresholds.maxCostPerRunUSD: must not be negative",
		},
		{
			name:    "maxDrop without a baseline",
			file:    "thresholds: { maxDrop: 0.02 }\n",
			wantErr: "thresholds.maxDrop: set thresholds.baseline too",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "bench.yaml")
			require.NoError(t, os.WriteFile(path, []byte(tt.file), 0o600))

			got, err := LoadSettings(path)
			if tt.wantErr != "" {
				require.ErrorIs(t, err, ErrInvalidSettings)
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got.Thresholds)
		})
	}
}

func TestWriteThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		results []ThresholdResult
		want    string
	}{
		{name: "no thresholds", want: ""},
		{
			name: "one line per threshold",
			results: []ThresholdResult{
				{Name: thresholdVerdictAccuracy, Limit: 0.8, Value: new(0.8), Passed: true},
				{Name: thresholdFPPrecision, Limit: 0.95, Passed: true, Detail: nothingToCount},
				{Name: thresholdMaxCostPerRun, Limit: 0.25, Value: new(0.3)},
			},
			want: "\nTHRESHOLD         LIMIT  VALUE  RESULT\n" +
				"verdictAccuracy   0.8    0.8    passed\n" +
				"fpPrecision       0.95   -      passed (nothing to count, so it passes)\n" +
				"maxCostPerRunUSD  0.25   0.3    FAILED\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer
			require.NoError(t, WriteThresholds(&out, tt.results))
			require.Equal(t, tt.want, out.String())
		})
	}
}

// countFailed counts the results that did not pass.
func countFailed(results []ThresholdResult) int {
	n := 0

	for _, r := range results {
		if !r.Passed {
			n++
		}
	}

	return n
}
