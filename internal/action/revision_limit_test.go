package action

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type rollbackConfig struct {
	ReadWriteGate `yaml:",inline"`
	RevisionLimit `yaml:",inline"`

	Name string `yaml:"name"`
}

func decodeRollbackConfig(t *testing.T, text string) (rollbackConfig, error) {
	t.Helper()

	var cfg rollbackConfig

	dec := yaml.NewDecoder(strings.NewReader(text))
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		return rollbackConfig{}, fmt.Errorf("decode the config: %w", err)
	}

	return cfg, nil
}

func TestRevisionLimitInAConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		yaml      string
		wantMax   int
		wantCheck string
	}{
		{name: "unset gives the default", yaml: "name: a\n", wantMax: 1},
		{name: "1", yaml: "maxRevisionsBack: 1\n", wantMax: 1},
		{name: "5 beside the gate", yaml: "allowReadWrite: true\nmaxRevisionsBack: 5\n", wantMax: 5},
		{name: "10", yaml: "maxRevisionsBack: 10\n", wantMax: 10},
		{name: "0 is an error, not the default", yaml: "maxRevisionsBack: 0\n", wantMax: 0, wantCheck: "maxRevisionsBack: 0 must be from 1 to 10"},
		{name: "negative", yaml: "maxRevisionsBack: -2\n", wantMax: -2, wantCheck: "maxRevisionsBack: -2 must be from 1 to 10"},
		{name: "11", yaml: "maxRevisionsBack: 11\n", wantMax: 11, wantCheck: "maxRevisionsBack: 11 must be from 1 to 10"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg, err := decodeRollbackConfig(t, tt.yaml)
			require.NoError(t, err)
			require.Equal(t, tt.wantMax, cfg.Max())

			if tt.wantCheck == "" {
				require.NoError(t, cfg.CheckConfig())

				return
			}

			require.EqualError(t, cfg.CheckConfig(), tt.wantCheck)
		})
	}
}

func TestRevisionLimitKeyIsUnknownWithoutIt(t *testing.T) {
	t.Parallel()

	var cfg struct {
		Name string `yaml:"name"`
	}

	dec := yaml.NewDecoder(strings.NewReader("maxRevisionsBack: 2\n"))
	dec.KnownFields(true)

	require.Error(t, dec.Decode(&cfg))
}

func TestRevisionLimitCheckRequest(t *testing.T) {
	t.Parallel()

	three := 3

	tests := []struct {
		name    string
		limit   RevisionLimit
		back    int
		wantErr error
	}{
		{name: "one back with the default", back: 1},
		{name: "two back with the default", back: 2, wantErr: ErrLimit},
		{name: "three back with a limit of three", limit: RevisionLimit{MaxRevisionsBack: &three}, back: 3},
		{name: "four back with a limit of three", limit: RevisionLimit{MaxRevisionsBack: &three}, back: 4, wantErr: ErrLimit},
		{name: "zero back", back: 0, wantErr: ErrParams},
		{name: "negative", back: -1, wantErr: ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.limit.CheckRequest(tt.back)
			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.wantErr)
		})
	}
}
