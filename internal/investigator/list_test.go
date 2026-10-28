package investigator

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestListTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                   string
		title, alertName, body string
		want                   string
	}{
		{name: "the title wins", title: "p99 high", alertName: "CheckoutLatency", body: "text", want: "p99 high"},
		{name: "the alert name without a title", alertName: "CheckoutLatency", body: "text", want: "CheckoutLatency"},
		{name: "the text without either", body: "Why is the queue backed up?", want: "Why is the queue backed up?"},
		{name: "nothing", want: ""},
		{name: "a long title is cut by characters", title: strings.Repeat("é", MaxListTitleChars+5), want: strings.Repeat("é", MaxListTitleChars)},
		{name: "a title at the limit stays", title: strings.Repeat("a", MaxListTitleChars), want: strings.Repeat("a", MaxListTitleChars)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, ListTitle(tt.title, tt.alertName, tt.body))
		})
	}
}
