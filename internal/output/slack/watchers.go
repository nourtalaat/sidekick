package slack

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/output"
)

// ActionStopWatcher starts the action ID of each Stop watching button,
// which is ActionStopWatcher, a colon and the button's number, since the
// buttons of one row need IDs of their own. Each button's value is the
// watcher ID. IsStopWatcher tells the buttons from others.
const ActionStopWatcher = "stop_watcher"

// The block IDs of the watcher line and the Stop watching buttons.
const (
	blockIDWatcher        = "watcher"
	blockIDWatcherButtons = "watchers"
)

// Bar colors of the short replies about watchers, from the status
// palette: watching is the info blue, back up the success green, and
// stopped the neutral gray. Timed out takes colorFailed, the warning
// yellow.
const (
	colorWatching = "#2C57CD"
	colorBackUp   = "#1F8A4C"
	colorStopped  = "#7C85A3"
)

// What the messages show about watchers, at most: lines in the main
// message, and characters of a name in a button, which Slack keeps to 75
// characters with the words around it.
const (
	maxWatcherLines     = 10
	maxWatcherNameChars = 60
)

// Seconds in a minute and in an hour, for check intervals.
const (
	secondsPerMinute = 60
	secondsPerHour   = 3600
)

// The stop reasons a watcher line explains, besides a responder.
var watcherStopReasons = map[string]string{
	"group_resolved": "every alert recovered",
	"config_changed": "the config no longer allows its check",
}

// watcherStyle is how a watcher state looks: an emoji and a label, always
// shown together with the bar color, which is never the only signal.
type watcherStyle struct {
	emoji string
	label string
	color string
}

// IsStopWatcher reports whether actionID is the action ID of a Stop
// watching button.
func IsStopWatcher(actionID string) bool {
	return strings.HasPrefix(actionID, ActionStopWatcher+":")
}

// watcherContext is the line of each watcher in the main message, oldest
// first, or no block when the group has no watchers.
func watcherContext(u output.Update) []block {
	if len(u.Watchers) == 0 {
		return nil
	}

	shown := u.Watchers[:min(len(u.Watchers), maxWatcherLines)]

	lines := make([]string, 0, len(shown)+1)
	for _, s := range shown {
		lines = append(lines, watcherLine(s))
	}

	if hidden := len(u.Watchers) - len(shown); hidden > 0 {
		lines = append(lines, fmt.Sprintf("%d more watchers", hidden))
	}

	b := contextBlock(strings.Join(lines, "\n"))
	b.BlockID = blockIDWatcher

	return []block{b}
}

// watcherButtons is the row with a Stop watching button for each running
// watcher the main message shows, or no block when none runs. Each asks
// before it stops the watcher.
func watcherButtons(u output.Update) []block {
	var buttons []button

	for _, s := range u.Watchers[:min(len(u.Watchers), maxWatcherLines)] {
		if s.Status != output.WatcherRunning {
			continue
		}

		name := cut(s.Name, maxWatcherNameChars)
		buttons = append(buttons, button{
			Type:     elemButton,
			ActionID: ActionStopWatcher + ":" + strconv.Itoa(len(buttons)+1),
			Text:     plain("Stop watching " + name),
			Value:    s.ID,
			Confirm: &confirmDialog{
				Title:   plain("Stop watching?"),
				Text:    plain("Sidekick stops checking " + name + ". The investigation and its verdict stay as they are."),
				Confirm: plain("Stop watching"),
				Deny:    plain("Keep watching"),
			},
		})
	}

	if len(buttons) == 0 {
		return nil
	}

	return []block{{Type: blockActions, BlockID: blockIDWatcherButtons, Buttons: buttons}}
}

// watcherReplyView renders the short thread reply about the change of
// u.Watcher, linking to the main message when permalink is set.
func watcherReplyView(u output.Update, permalink string) view {
	s := *u.Watcher
	style := styleOfWatcher(s)
	title := eventTitle(u)

	parts := []string{escape(title)}
	if s.Checks > 0 {
		parts = append(parts, "Checked every "+every(s.IntervalSeconds)+", "+checks(s.Checks))
	}

	if s.LastResult != "" {
		parts = append(parts, "Last check: "+escape(cut(s.LastResult, maxLineChars)))
	}

	parts = append(parts, link(permalink, "See the investigation"))

	return view{
		text: escape(style.label + " " + cut(s.Name, maxTitleChars) + ": " + title),
		attachment: attachment{Color: style.color, Blocks: []block{
			section(watcherLine(s)),
			contextBlock(joinParts(parts...)),
		}},
	}
}

// watcherLine is one watcher's state, such as ":eyes: *Watching*
// checkout-health · checking every 30s since 14:02". Its times are shown
// as times of day, never as durations up to now, so the line stays true
// between edits.
func watcherLine(s output.WatcherState) string {
	style := styleOfWatcher(s)
	head := style.emoji + " *" + style.label + "* " + escape(cut(s.Name, maxTitleChars))

	switch s.Status {
	case output.WatcherRecovered:
		return head + " · at " + slackTime(s.EndedAt) + ", watched for " + minutes(s.EndedAt.Sub(s.StartedAt))
	case output.WatcherTimedOut:
		return head + " · after " + minutes(s.EndedAt.Sub(s.StartedAt)) + ", still down"
	case output.WatcherStopped:
		return head + " · " + stopText(s) + " at " + slackTime(s.EndedAt)
	default:
		return head + " · checking every " + every(s.IntervalSeconds) + " since " + slackTime(s.StartedAt)
	}
}

// styleOfWatcher returns the style of the state of s.
func styleOfWatcher(s output.WatcherState) watcherStyle {
	switch s.Status {
	case output.WatcherRecovered:
		return watcherStyle{emoji: ":white_check_mark:", label: "Back up", color: colorBackUp}
	case output.WatcherTimedOut:
		return watcherStyle{emoji: ":hourglass:", label: "Stopped watching", color: colorFailed}
	case output.WatcherStopped:
		return watcherStyle{emoji: ":black_square_for_stop:", label: "Stopped watching", color: colorStopped}
	default:
		return watcherStyle{emoji: ":eyes:", label: "Watching", color: colorWatching}
	}
}

// stopText says why s stopped: who stopped it, or the reason.
func stopText(s output.WatcherState) string {
	if reason, ok := watcherStopReasons[s.StopReason]; ok {
		return reason + ","
	}

	if s.StoppedBy != nil {
		if who := mention(*s.StoppedBy); who != "" {
			return "stopped by " + who
		}
	}

	return "stopped by a responder"
}

// every is a check interval of seconds, such as "30s", "2 min" or "1h".
func every(seconds int) string {
	switch {
	case seconds >= secondsPerHour && seconds%secondsPerHour == 0:
		return fmt.Sprintf("%dh", seconds/secondsPerHour)
	case seconds >= secondsPerMinute && seconds%secondsPerMinute == 0:
		return fmt.Sprintf("%d min", seconds/secondsPerMinute)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}

// minutes is d in whole minutes, at least one, such as "12 min".
func minutes(d time.Duration) string {
	return fmt.Sprintf("%d min", max(1, int(d.Round(time.Minute).Minutes())))
}

// checks is a number of checks, such as "1 check" or "24 checks".
func checks(n int) string {
	if n == 1 {
		return "1 check"
	}

	return fmt.Sprintf("%d checks", n)
}
