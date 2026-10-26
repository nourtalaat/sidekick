package kubernetes

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

func TestListEvents(t *testing.T) {
	t.Parallel()

	backOff := event("prod", "backoff", "Warning", "BackOff", "Pod", "checkout-1", "Back-off restarting failed container", 10*time.Minute, 12)
	oom := event("prod", "oom", "Warning", "OOMKilling", "Node", "node-1", "Memory cgroup out of memory", 30*time.Minute, 1)
	scaled := event("prod", "scaled", "Normal", "ScalingReplicaSet", "Deployment", "checkout", "Scaled up replica set checkout-7d9 to 3", 2*time.Hour, 1)
	staging := event("staging", "pulled", "Normal", "Pulled", "Pod", "web-1", "Container image pulled", 5*time.Minute, 1)
	series := event("prod", "series", "Warning", "Unhealthy", "Pod", "checkout-2", "Readiness probe failed", 3*time.Hour, 0)
	series.LastTimestamp = metav1.Time{}
	series.Series = &corev1.EventSeries{Count: 40, LastObservedTime: metav1.MicroTime(at(time.Minute))}

	objects := []runtime.Object{backOff, oom, scaled, staging, series}

	tests := []struct {
		name       string
		cfg        Config
		reactors   []reactor
		input      string
		wantNames  []string
		wantText   []string
		wantNoData bool
		wantTool   []string
		wantErr    error
		wantLinks  []string
	}{
		{
			name:      "every namespace, newest first",
			input:     `{}`,
			wantNames: []string{"checkout-2", "web-1", "checkout-1", "node-1", "checkout"},
			wantText: []string{
				"5 events in every namespace, newest first:",
				"2026-10-26T11:59:00Z Warning Unhealthy Pod prod/checkout-2 (x40): Readiness probe failed",
				"2026-10-26T11:50:00Z Warning BackOff Pod prod/checkout-1 (x12): Back-off restarting failed container",
			},
		},
		{name: "one namespace", input: `{"namespace":"staging"}`, wantNames: []string{"web-1"}},
		{name: "the configured namespaces only", cfg: Config{Namespaces: []string{"staging"}}, input: `{}`, wantNames: []string{"web-1"}},
		{name: "one object, any case of kind", input: `{"kind":"pod","name":"checkout-1"}`, wantNames: []string{"checkout-1"}},
		{name: "warnings since an hour ago", input: `{"type":"Warning","since":"1h"}`, wantNames: []string{"checkout-2", "checkout-1", "node-1"}},
		{name: "since a time", input: `{"since":"2026-10-26T11:52:00Z"}`, wantNames: []string{"checkout-2", "web-1"}},
		{
			name:      "a limit",
			input:     `{"limit":2}`,
			wantNames: []string{"checkout-2", "web-1"},
			wantText:  []string{"The newest 2 of 5 events in every namespace:"},
		},
		{
			name:      "console links for the objects",
			cfg:       Config{ConsoleURL: testConsoleURL},
			input:     `{"namespace":"prod","type":"Warning"}`,
			wantNames: []string{"checkout-2", "checkout-1", "node-1"},
			wantLinks: []string{
				"https://console.example.com/pods/prod/checkout-2",
				"https://console.example.com/pods/prod/checkout-1",
				"https://console.example.com/nodes//node-1",
			},
		},
		{name: "no events", input: `{"namespace":"prod","name":"missing"}`, wantNoData: true},
		{
			name:     "a namespace the config does not list",
			cfg:      Config{Namespaces: []string{"staging"}},
			input:    `{"namespace":"prod"}`,
			wantTool: []string{`namespace "prod" is not one Sidekick may read; use one of staging`},
		},
		{name: "a bad since", input: `{"since":"yesterday"}`, wantTool: []string{`since: "yesterday" is neither`}},
		{
			name:     "a missing permission",
			reactors: []reactor{failWith("list", "events", forbidden("", "events"))},
			input:    `{"namespace":"prod"}`,
			wantTool: []string{`may not list events in namespace prod`, `the "list" verb on "events" in the core API group`},
		},
		{
			name:     "an API server that cannot be reached",
			reactors: []reactor{failWith("list", "events", errConnection)},
			input:    `{}`,
			wantErr:  errConnection,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newTestSource(t, tt.cfg, objects, tt.reactors...)

			res, err := call(t, s, toolListEvents, tt.input)

			switch {
			case tt.wantErr != nil:
				require.ErrorIs(t, err, tt.wantErr)

				return
			case tt.wantTool != nil:
				requireToolError(t, err, tt.wantTool...)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantNoData, res.NoData)
			require.NotEmpty(t, res.Evidence)

			if tt.wantNoData {
				require.Empty(t, res.Text)

				return
			}

			var out eventsOutput
			require.NoError(t, json.Unmarshal(res.Structured, &out))

			names := make([]string, 0, len(out.Events))
			for _, e := range out.Events {
				names = append(names, e.Name)
			}

			require.Equal(t, tt.wantNames, names)

			for _, want := range tt.wantText {
				require.Contains(t, res.Text, want)
			}

			if tt.wantLinks != nil {
				links := make([]string, 0, len(res.Evidence))
				for _, e := range res.Evidence {
					links = append(links, e.URL)
				}

				require.Equal(t, tt.wantLinks, links)
			}
		})
	}
}

func TestEventsCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		namespaces []string
		in         eventsInput
		want       string
	}{
		{name: "every namespace", namespaces: []string{""}, want: "kubectl get events -A --sort-by=.lastTimestamp"},
		{name: "several namespaces", namespaces: []string{"prod", "staging"}, want: "kubectl get events -A --sort-by=.lastTimestamp"},
		{
			name:       "one object",
			namespaces: []string{"prod"},
			in:         eventsInput{Kind: "Pod", Name: "checkout-1", Type: "Warning"},
			want:       "kubectl get events -n prod --field-selector involvedObject.kind=Pod,involvedObject.name=checkout-1,type=Warning --sort-by=.lastTimestamp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, eventsCommand(tt.namespaces, tt.in))
		})
	}
}

// event returns an event about one object that last happened ago before
// testNow.
func event(ns, name, typ, reason, kind, object, message string, ago time.Duration, count int32) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Namespace: ns, Name: name, CreationTimestamp: at(ago)},
		InvolvedObject: corev1.ObjectReference{Kind: kind, Namespace: ns, Name: object},
		Type:           typ,
		Reason:         reason,
		Message:        message,
		Count:          count,
		FirstTimestamp: at(ago),
		LastTimestamp:  at(ago),
	}
}
