package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestPeriod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		yaml       string
		want       Period
		wantString string
		wantErr    bool
	}{
		{name: "days", yaml: "90d", want: Period(90 * Day), wantString: "90d"},
		{name: "go duration", yaml: "36h", want: Period(36 * time.Hour), wantString: "36h0m0s"},
		{name: "go duration of whole days", yaml: "48h", want: Period(2 * Day), wantString: "2d"},
		{name: "zero days", yaml: "0d", want: 0, wantString: "0s"},
		{name: "days with a space", yaml: "90 d", wantErr: true},
		{name: "fractional days", yaml: "1.5d", wantErr: true},
		{name: "no unit", yaml: "90", wantErr: true},
		{name: "a list", yaml: "[90d]", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var p Period

			err := yaml.Unmarshal([]byte(tt.yaml), &p)
			if tt.wantErr {
				require.ErrorIs(t, err, errBadPeriod)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, p)
			require.Equal(t, tt.wantString, p.String())
		})
	}
}
