package grouper

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
)

func TestFingerprint(t *testing.T) {
	t.Parallel()

	alert := func(labels map[string]string) event.Event {
		return event.Event{Intake: "alertmanager", Kind: event.KindAlert, AlertName: "KubePodCrashLooping", Labels: labels, Fingerprint: "stored"}
	}

	keys := []string{"service", "env"}
	prodCheckout := event.Fingerprint("alertmanager", "KubePodCrashLooping", map[string]string{"service": "checkout", "env": "prod"}, keys)
	require.NotEqual(t, prodCheckout, event.Fingerprint("alertmanager", "KubePodCrashLooping", map[string]string{"service": "checkout", "env": ""}, keys),
		"a missing label is not the same as a set one")

	tests := []struct {
		name string
		ev   event.Event
		keys []string
		want string
	}{
		{
			name: "the chosen labels",
			ev:   alert(map[string]string{"service": "checkout", "env": "prod", "pod": "checkout-1"}),
			keys: keys,
			want: prodCheckout,
		},
		{
			name: "other labels do not change it",
			ev:   alert(map[string]string{"service": "checkout", "env": "prod", "pod": "checkout-2", "instance": "10.0.0.9"}),
			keys: keys,
			want: prodCheckout,
		},
		{
			name: "the order of the chosen labels does not change it",
			ev:   alert(map[string]string{"service": "checkout", "env": "prod"}),
			keys: []string{"env", "service"},
			want: prodCheckout,
		},
		{
			name: "a chosen label with another value",
			ev:   alert(map[string]string{"service": "checkout", "env": "staging"}),
			keys: keys,
			want: event.Fingerprint("alertmanager", "KubePodCrashLooping", map[string]string{"service": "checkout", "env": "staging"}, keys),
		},
		{
			name: "a missing chosen label counts as empty",
			ev:   alert(map[string]string{"service": "checkout"}),
			keys: keys,
			want: event.Fingerprint("alertmanager", "KubePodCrashLooping", map[string]string{"service": "checkout", "env": ""}, keys),
		},
		{
			name: "every chosen label missing keeps the stored fingerprint",
			ev:   alert(map[string]string{"pod": "checkout-1"}),
			keys: keys,
			want: "stored",
		},
		{
			name: "no chosen labels keeps the stored fingerprint",
			ev:   alert(map[string]string{"service": "checkout"}),
			want: "stored",
		},
		{
			name: "a message keeps the stored fingerprint",
			ev:   event.Event{Intake: "slack-alerts", Kind: event.KindMessage, Labels: map[string]string{"service": "checkout"}, Fingerprint: "stored"},
			keys: keys,
			want: "stored",
		},
		{
			name: "another intake gives another fingerprint",
			ev:   event.Event{Intake: "alertmanager-eu", Kind: event.KindAlert, AlertName: "KubePodCrashLooping", Labels: map[string]string{"service": "checkout", "env": "prod"}},
			keys: keys,
			want: event.Fingerprint("alertmanager-eu", "KubePodCrashLooping", map[string]string{"service": "checkout", "env": "prod"}, keys),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, fingerprint(tt.ev, tt.keys))
		})
	}
}
