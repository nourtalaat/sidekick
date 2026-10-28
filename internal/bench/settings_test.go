package bench

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// file is the content of bench.yaml; empty means no file.
		file        string
		wantWeights Weights
		wantErr     string
	}{
		{
			name:        "no file gives the defaults",
			wantWeights: DefaultWeights,
		},
		{
			name:        "weights of its own",
			file:        "weights: { verdict: 0.6, rootCause: 0.4, evidence: 0 }\n",
			wantWeights: Weights{Verdict: 0.6, RootCause: 0.4},
		},
		{
			name:        "a file without weights keeps the default ones",
			file:        "{}\n",
			wantWeights: DefaultWeights,
		},
		{
			name:    "a negative weight",
			file:    "weights: { verdict: -1, rootCause: 2 }\n",
			wantErr: "weights.verdict: must not be negative",
		},
		{
			name:    "weights that are all 0",
			file:    "weights: { verdict: 0 }\n",
			wantErr: "weights: at least one weight must be more than 0",
		},
		{
			name:    "an unknown key",
			file:    "weight: { verdict: 1 }\n",
			wantErr: "weight: unknown key",
		},
		{
			name:    "an empty file",
			file:    "\n",
			wantErr: "the file is empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := ""
			if tt.file != "" {
				path = filepath.Join(t.TempDir(), "bench.yaml")
				require.NoError(t, os.WriteFile(path, []byte(tt.file), 0o600))
			}

			got, err := LoadSettings(path)
			if tt.wantErr != "" {
				require.ErrorIs(t, err, ErrInvalidSettings)
				require.ErrorContains(t, err, path+": "+tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantWeights, got.ScoreWeights())
		})
	}
}

func TestLoadSettingsMissingFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a file that does not exist"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := LoadSettings(filepath.Join(t.TempDir(), "missing.yaml"))
			require.ErrorIs(t, err, ErrInvalidSettings)
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}
