package kubernetes

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/source"
)

// eventsInput is the input of list_events.
type eventsInput struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Since     string `json:"since"`
	Limit     int    `json:"limit"`
}

// eventsOutput is the structured result of list_events.
type eventsOutput struct {
	Events []eventView `json:"events"`
	// Matched is how many events matched, before the limit.
	Matched int `json:"matched"`
}

// eventView is one event.
type eventView struct {
	Namespace string    `json:"namespace"`
	Type      string    `json:"type"`
	Reason    string    `json:"reason"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Message   string    `json:"message"`
	Count     int32     `json:"count"`
	FirstSeen time.Time `json:"firstSeen,omitzero"`
	LastSeen  time.Time `json:"lastSeen,omitzero"`
}

// listEvents lists the events that match the input, newest first.
func (s *Source) listEvents(ctx context.Context, c kubernetes.Interface, in eventsInput) (source.Result, error) {
	namespaces, err := s.scope(in.Namespace)
	if err != nil {
		return source.Result{}, fmt.Errorf("list events: %w", err)
	}

	after, err := s.since(in.Since)
	if err != nil {
		return source.Result{}, fmt.Errorf("list events: %w", err)
	}

	opts := metav1.ListOptions{}
	if in.Name != "" {
		opts.FieldSelector = fields.OneTermEqualSelector("involvedObject.name", in.Name).String()
	}

	var views []eventView

	for _, ns := range namespaces {
		err := eachPage(ctx, opts, c.CoreV1().Events(ns).List, func(page *corev1.EventList) {
			for i := range page.Items {
				if v, ok := matchEvent(&page.Items[i], in, after); ok {
					views = append(views, v)
				}
			}
		})
		if err != nil {
			return source.Result{}, apiError("list", resEvents, ns, err)
		}
	}

	slices.SortStableFunc(views, func(a, b eventView) int {
		return cmp.Or(b.LastSeen.Compare(a.LastSeen), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
	})

	out := eventsOutput{Matched: len(views), Events: views[:min(len(views), limit(in.Limit, defaultEvents, maxEvents))]}
	evidence := s.links.evidence(source.EvidenceResource, eventObjects(out.Events), eventsCommand(namespaces, in))

	if len(out.Events) == 0 {
		return source.Result{NoData: true, Evidence: evidence}, nil
	}

	return result(eventsText(namespaces, out), out, evidence, false)
}

// matchEvent returns the view of an event that matches the input's kind,
// name, type and since.
func matchEvent(e *corev1.Event, in eventsInput, after time.Time) (eventView, bool) {
	v := eventView{
		Namespace: e.Namespace,
		Type:      e.Type,
		Reason:    e.Reason,
		Kind:      e.InvolvedObject.Kind,
		Name:      e.InvolvedObject.Name,
		Message:   strings.TrimSpace(e.Message),
		Count:     max(e.Count, 1),
		FirstSeen: firstSeen(e),
		LastSeen:  lastSeen(e),
	}

	if e.Series != nil && e.Series.Count > 0 {
		v.Count = e.Series.Count
	}

	switch {
	case in.Kind != "" && !strings.EqualFold(in.Kind, v.Kind):
		return eventView{}, false
	case in.Name != "" && in.Name != v.Name:
		return eventView{}, false
	case in.Type != "" && !strings.EqualFold(in.Type, v.Type):
		return eventView{}, false
	case !after.IsZero() && v.LastSeen.Before(after):
		return eventView{}, false
	default:
		return v, true
	}
}

// firstSeen is when the event first happened, from whichever time field
// the reporter set.
func firstSeen(e *corev1.Event) time.Time {
	switch {
	case !e.FirstTimestamp.IsZero():
		return e.FirstTimestamp.Time
	case !e.EventTime.IsZero():
		return e.EventTime.Time
	default:
		return e.CreationTimestamp.Time
	}
}

// lastSeen is when the event last happened.
func lastSeen(e *corev1.Event) time.Time {
	switch {
	case e.Series != nil && !e.Series.LastObservedTime.IsZero():
		return e.Series.LastObservedTime.Time
	case !e.LastTimestamp.IsZero():
		return e.LastTimestamp.Time
	default:
		return firstSeen(e)
	}
}

// eventsText renders the events, one per line.
func eventsText(namespaces []string, out eventsOutput) string {
	var b strings.Builder

	if out.Matched > len(out.Events) {
		fmt.Fprintf(&b, "The newest %d of %s in %s:\n", len(out.Events), plural(out.Matched, "event"), where(namespaces))
	} else {
		fmt.Fprintf(&b, "%s in %s, newest first:\n", plural(out.Matched, "event"), where(namespaces))
	}

	for _, e := range out.Events {
		fmt.Fprintf(&b, "%s %s %s %s %s/%s (x%d): %s\n", timestamp(e.LastSeen), e.Type, e.Reason, e.Kind, e.Namespace, e.Name, e.Count, e.Message)
	}

	return b.String()
}

// eventObjects returns each object the events are about, once, in order.
func eventObjects(events []eventView) []object {
	var objects []object

	seen := map[object]bool{}

	for _, e := range events {
		o := object{Kind: e.Kind, Resource: resourceOf(e.Kind), Namespace: e.Namespace, Name: e.Name}
		if e.Kind == kindNode {
			o.Namespace = ""
		}

		if !seen[o] && o.Kind != "" && o.Name != "" {
			seen[o] = true
			objects = append(objects, o)
		}
	}

	return objects
}

// eventsCommand is the kubectl command that lists the same events.
func eventsCommand(namespaces []string, in eventsInput) string {
	args := append([]string{"get", "events"}, namespaceArgs(commandNamespace(namespaces))...)

	var selectors []string
	if in.Kind != "" {
		selectors = append(selectors, "involvedObject.kind="+in.Kind)
	}

	if in.Name != "" {
		selectors = append(selectors, "involvedObject.name="+in.Name)
	}

	if in.Type != "" {
		selectors = append(selectors, "type="+in.Type)
	}

	if len(selectors) > 0 {
		args = append(args, "--field-selector", strings.Join(selectors, ","))
	}

	return kubectl(append(args, "--sort-by=.lastTimestamp")...)
}
