package strategy

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchFits(t *testing.T) {
	t.Parallel()

	latency := Match{AlertNames: []string{"*Latency*", "*p99*"}, TextPattern: "(?i)latency|slow"}

	tests := []struct {
		name      string
		match     Match
		alertName string
		text      string
		want      bool
	}{
		{name: "an alert name glob", match: latency, alertName: "CheckoutLatencyP99", want: true},
		{name: "the text pattern", match: latency, alertName: "CheckoutP95", text: "Checkout is SLOW", want: true},
		{name: "neither", match: latency, alertName: "DiskFull", text: "disk at 93%"},
		{name: "no alert name and no text", match: latency},
		{name: "an empty match fits nothing", alertName: "CheckoutLatencyP99", text: "latency"},
		{name: "a glob that does not parse is skipped", match: Match{AlertNames: []string{"[", "Disk*"}}, alertName: "DiskFull", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.match.Fits(tt.alertName, tt.text))
		})
	}
}
