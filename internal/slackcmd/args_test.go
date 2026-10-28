package slackcmd

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
)

func TestSplitArgs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		text string
		want []string
	}{
		{name: "empty", text: "  ", want: nil},
		{name: "words", text: "route set checkout priority 5", want: []string{"route", "set", "checkout", "priority", "5"}},
		{name: "a quoted value", text: `listen #ops pattern:"disk full"`, want: []string{"listen", "#ops", "pattern:disk full"}},
		{name: "curly quotes", text: "listen #ops pattern:“disk full”", want: []string{"listen", "#ops", "pattern:disk full"}},
		{name: "empty quotes", text: `route set x text ""`, want: []string{"route", "set", "x", "text", ""}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, splitArgs(tt.text))
		})
	}
}

func TestChannelArg(t *testing.T) {
	t.Parallel()

	tests := []struct {
		arg         string
		wantChannel string
		wantName    string
	}{
		{arg: "<#C01OPS|ops>", wantChannel: "C01OPS", wantName: "ops"},
		{arg: "<#C01OPS|>", wantChannel: "C01OPS"},
		{arg: "<#G01PRIVATE>", wantChannel: "G01PRIVATE"},
		{arg: "#alerts-prod", wantChannel: "#alerts-prod", wantName: "alerts-prod"},
		{arg: "C01OPS", wantChannel: "C01OPS"},
	}

	for _, tt := range tests {
		t.Run(tt.arg, func(t *testing.T) {
			t.Parallel()

			channel, name := channelArg(tt.arg)
			require.Equal(t, tt.wantChannel, channel)
			require.Equal(t, tt.wantName, name)
		})
	}
}

func TestListenerName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		channel string
		want    string
	}{
		{channel: "alerts", want: "alerts"},
		{channel: "Alerts_Prod.EU", want: "alerts-prod-eu"},
		{channel: "2024-incidents", want: "incidents"},
		{channel: "a-very-long-channel-name-for-alerts", want: "a-very-long-channel-name"},
		{channel: "", want: ""},
		{channel: "___", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.channel, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, listenerName(tt.channel))
		})
	}
}

func TestParseListen(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		wantName string
		want     config.Listener
		wantErr  string
	}{
		{
			name:     "a channel only",
			args:     []string{"#ops"},
			wantName: "ops",
			want:     config.Listener{Channel: "#ops"},
		},
		{
			name:     "a bot name, a bot ID, a pattern and a name",
			args:     []string{"<#C01OPS|ops>", "bot:Grafana", "bot:B01GRAFANA", "pattern:(?i)firing", "name:grafana-ops"},
			wantName: "grafana-ops",
			want: config.Listener{Channel: "C01OPS", Match: config.ListenerMatch{
				Sender: config.ListenerSender{Kind: event.SenderBot, Names: []string{"Grafana"}, Bots: []string{"B01GRAFANA"}},
				Text:   "(?i)firing",
			}},
		},
		{name: "no channel", wantErr: "Usage"},
		{name: "an unknown option", args: []string{"#ops", "user:U01"}, wantErr: "`user:` is not an option."},
		{name: "not an option", args: []string{"#ops", "firing"}, wantErr: "`firing` is not an option."},
		{name: "a channel ID gives no name", args: []string{"C01OPS"}, wantErr: "Give one with `name:<name>`."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			name, l, err := parseListen(tt.args)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantName, name)
			require.Equal(t, tt.want, l)
		})
	}
}
