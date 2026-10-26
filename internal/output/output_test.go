package output

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWatcherStateEventName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status WatcherStatus
		want   string
	}{
		{status: WatcherRunning, want: "watcher.started"},
		{status: WatcherRecovered, want: "watcher.recovered"},
		{status: WatcherTimedOut, want: "watcher.timed_out"},
		{status: WatcherStopped, want: "watcher.stopped"},
	}

	for _, tt := range tests {
		t.Run(string(tt.status), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, WatcherState{Status: tt.status}.EventName())
		})
	}
}
