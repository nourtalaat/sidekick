package slackcmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
)

func TestDescribeRouteListsTheDetailKeysOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		details map[string]string
		want    string
	}{
		{name: "no details", want: "matches every event; strategy `generic-alert`, mode `read-only`, outputs `slack-thread`"},
		{
			name:    "details, sorted by key and without their values",
			details: map[string]string{"owner": "payments team", "environment": "prod"},
			want:    "matches every event; strategy `generic-alert`, mode `read-only`, outputs `slack-thread`, details `environment`, `owner`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := config.NewRoute("checkout")
			r.Outputs = []string{"slack-thread"}
			r.Details = tt.details

			require.Equal(t, tt.want, describeRoute(r, true))
		})
	}
}
