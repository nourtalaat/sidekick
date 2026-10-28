package slack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
)

func TestWatcherBlocksInEveryView(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		update output.Update
		// want are the type and block ID of each block from the watcher
		// line on.
		want []string
	}{
		{
			name:   "queued",
			update: testUpdate(output.PhaseQueued, nil),
			want:   []string{"context watcher", "actions run", "actions watchers"},
		},
		{
			name:   "running",
			update: runningUpdate(2),
			want:   []string{"context watcher", "actions run", "actions watchers"},
		},
		{
			name:   "final",
			update: testUpdate(output.PhaseFinal, verdictReport(report.TruePositive)),
			want:   []string{"context watcher", "actions feedback", "actions watchers"},
		},
		{
			name:   "failed",
			update: testUpdate(output.PhaseFinal, &report.Report{StopReason: report.StopError}),
			want:   []string{"context watcher", "actions watchers"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			without := mainView(tt.update)

			u := tt.update
			u.Watchers = testWatchers()
			blocks := mainView(u).attachment.Blocks

			var got []string

			for i, b := range blocks {
				if b.BlockID == blockIDWatcher {
					for _, rest := range blocks[i:] {
						got = append(got, rest.Type+" "+rest.BlockID)
					}

					break
				}
			}

			require.Equal(t, tt.want, got)
			require.Len(t, blocks, len(without.attachment.Blocks)+2, "the watchers add their line and their buttons, nothing else")
		})
	}
}

func TestWatcherLinesAreCapped(t *testing.T) {
	t.Parallel()

	u := testUpdate(output.PhaseRunning, nil)

	for i := range maxWatcherLines + 2 {
		w := testWatchers()[0]
		w.ID, w.Name = fmt.Sprintf("w%d", i+1), fmt.Sprintf("watcher-%d", i+1)
		u.Watchers = append(u.Watchers, w)
	}

	lines := strings.Split(watcherContext(u)[0].Elements[0].Text, "\n")
	require.Len(t, lines, maxWatcherLines+1)
	require.Equal(t, "2 more watchers", lines[maxWatcherLines])

	buttons := watcherButtons(u)[0].Buttons
	require.Len(t, buttons, maxWatcherLines, "only the watchers shown get a button")

	for i, b := range buttons {
		require.Equal(t, fmt.Sprintf("%s:%d", ActionStopWatcher, i+1), b.ActionID, "the buttons of one row have IDs of their own")
		require.True(t, IsStopWatcher(b.ActionID))
	}
}

func TestNoWatcherBlocksWithoutRunningWatchers(t *testing.T) {
	t.Parallel()

	u := testUpdate(output.PhaseRunning, nil)
	require.Empty(t, watcherContext(u))
	require.Empty(t, watcherButtons(u))

	u.Watchers = testWatchers()[1:]
	require.Len(t, watcherContext(u), 1, "ended watchers keep their line")
	require.Empty(t, watcherButtons(u), "only a running watcher can be stopped")
}

func TestWatcherNamesAreEscaped(t *testing.T) {
	t.Parallel()

	w := testWatchers()[0]
	w.Name = "<!channel> health"

	u := testUpdate(output.PhaseWatcher, nil)
	u.Watcher, u.Watchers = &w, []output.WatcherState{w}

	require.Contains(t, watcherContext(u)[0].Elements[0].Text, "&lt;!channel&gt; health")

	reply := watcherReplyView(u, "")
	require.Contains(t, reply.text, "&lt;!channel&gt; health")
	require.Contains(t, reply.attachment.Blocks[0].Text.Text, "&lt;!channel&gt; health")
	require.Equal(t, "Stop watching <!channel> health", watcherButtons(u)[0].Buttons[0].Text.Text, "plain text is shown as it is")
}

func TestIsStopWatcher(t *testing.T) {
	t.Parallel()

	tests := []struct {
		actionID string
		want     bool
	}{
		{actionID: "stop_watcher:1", want: true},
		{actionID: "stop_watcher:12", want: true},
		{actionID: "stop_watcher"},
		{actionID: "stop_watchers:1"},
		{actionID: ActionStop},
	}

	for _, tt := range tests {
		t.Run(tt.actionID, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, IsStopWatcher(tt.actionID))
		})
	}
}

func TestEvery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		seconds int
		want    string
	}{
		{seconds: 10, want: "10s"},
		{seconds: 30, want: "30s"},
		{seconds: 60, want: "1 min"},
		{seconds: 90, want: "90s"},
		{seconds: 900, want: "15 min"},
		{seconds: 3600, want: "1h"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, every(tt.seconds))
		})
	}
}
