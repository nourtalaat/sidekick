package unleash

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// searchEventsPath is the event search API.
const searchEventsPath = "/api/admin/search/events"

// eventPageSize is the most events one request asks for. Unleash allows
// 1000, but the events hold a flag's configuration before and after, so
// pages are kept small.
const eventPageSize = 100

// maxEventPages bounds the pages one call reads. flags.MaxChanges events
// fit in fewer; the bound only stops a service that keeps answering, or a
// window that ends long before the day Unleash filters on.
const maxEventPages = 10

// maxEvidence bounds the flag links one list_flag_changes result gives.
const maxEvidence = 10

// dayLayout is the date format of the from and to filters.
const dayLayout = time.DateOnly

// markdownLink matches a Markdown link or image, such as [text](url).
var markdownLink = regexp.MustCompile(`!?\[([^\]]*)\]\(([^)\s]*)\)`)

// eventPage is one page of the event search.
type eventPage struct {
	Events []event `json:"events"`
	Total  int     `json:"total"`
}

// event is the part of an Unleash event the source reads.
type event struct {
	ID          int64     `json:"id"`
	CreatedAt   time.Time `json:"createdAt"`
	Type        string    `json:"type"`
	CreatedBy   string    `json:"createdBy"`
	Environment string    `json:"environment"`
	Project     string    `json:"project"`
	FeatureName string    `json:"featureName"`
	Label       string    `json:"label"`
	Summary     string    `json:"summary"`
}

// listFlagChanges runs list_flag_changes.
func (s *Source) listFlagChanges(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := flags.Decode[flags.ChangesInput](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	scope, err := s.cfg.Projects.Scope(in.Project, in.Environment)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	flag := strings.TrimSpace(in.Flag)
	if flag != "" {
		if err := flags.CheckKey("flag", flag, keyPattern); err != nil {
			return source.Result{}, fmt.Errorf("read the input: %w", err)
		}
	}

	w, err := flags.ParseWindow(in.Since, in.Until, s.now())
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	events, more, err := s.events(ctx, scope, flag, w, flags.Limit(in.Limit))
	if err != nil {
		return source.Result{}, fmt.Errorf("read the events of %s/%s: %w", scope.Project, scope.Environment, err)
	}

	list := flags.NewChangeList(scope, flag, w)

	var keys []string

	for _, e := range events {
		c := s.change(e, scope)
		if !list.Add(c) {
			break
		}

		if c.Flag != "" && keyPattern.MatchString(c.Flag) && !slices.Contains(keys, c.Flag) {
			keys = append(keys, c.Flag)
		}
	}

	if flag != "" {
		keys = []string{flag}
	}

	res, err := list.Result(service, more, s.flagEvidence(scope, keys))
	if err != nil {
		return source.Result{}, fmt.Errorf("make the result: %w", err)
	}

	return res, nil
}

// events reads the events in w of the flags of scope, or of one flag,
// newest first, up to limit, and reports whether there are more. Unleash
// filters events by whole days, so the request asks for the days around w
// and the events outside w are dropped here. It pages with an offset; the
// events are sorted by time, so it stops at the first one before w, and
// events that moved to a later page while it read are skipped by ID.
func (s *Source) events(ctx context.Context, scope flags.Scope, flag string, w flags.Window, limit int) ([]event, bool, error) {
	var out []event

	seen := map[int64]bool{}
	offset := 0

	for range maxEventPages {
		want := min(eventPageSize, limit+1-len(out))

		var page eventPage
		if err := s.client.get(ctx, searchEventsPath, eventQuery(scope, flag, w, want, offset), &page); err != nil {
			return nil, false, fmt.Errorf("read a page of events: %w", err)
		}

		for _, e := range page.Events {
			switch {
			case e.CreatedAt.Before(w.Start):
				return out, false, nil
			case !e.CreatedAt.Before(w.End) || seen[e.ID] || !inScope(e, scope):
				continue
			case len(out) == limit:
				return out, true, nil
			}

			seen[e.ID] = true
			out = append(out, e)
		}

		offset += len(page.Events)

		if len(page.Events) < want || offset >= page.Total {
			return out, false, nil
		}
	}

	return out, true, nil
}

// eventQuery returns the query of an event search for the flags of scope,
// or one flag, in the days around w. Every key in it has been checked
// against keyPattern, so none can add a value or change an operator.
func eventQuery(scope flags.Scope, flag string, w flags.Window, limit, offset int) url.Values {
	query := url.Values{
		"project":     {"IS:" + scope.Project},
		"environment": {"IS:" + scope.Environment},
		"from":        {"IS:" + w.Start.UTC().AddDate(0, 0, -1).Format(dayLayout)},
		"to":          {"IS:" + w.End.UTC().AddDate(0, 0, 1).Format(dayLayout)},
		"limit":       {strconv.Itoa(limit)},
		"offset":      {strconv.Itoa(offset)},
	}

	if flag != "" {
		query.Set("feature", "IS:"+flag)
	}

	return query
}

// inScope reports whether the event is of the project and environment of
// scope, when it names them.
func inScope(e event, scope flags.Scope) bool {
	return (e.Project == "" || e.Project == scope.Project) && (e.Environment == "" || e.Environment == scope.Environment)
}

// change returns the change an event records. The action is Unleash's event
// type, such as feature-environment-enabled. Unleash events carry no
// comment.
func (s *Source) change(e event, scope flags.Scope) flags.Change {
	c := flags.Change{
		Time:    e.CreatedAt.UTC(),
		Flag:    e.FeatureName,
		Action:  e.Type,
		Actor:   e.CreatedBy,
		Summary: s.ownLinks(strings.TrimSpace(e.Summary)),
	}

	if c.Summary == "" {
		c.Summary = s.ownLinks(strings.TrimSpace(e.Label))
	}

	if e.FeatureName != "" && keyPattern.MatchString(e.FeatureName) {
		c.URL = s.flagURL(scope.Project, e.FeatureName)
	}

	return c
}

// ownLinks returns text with each Markdown link that points to a host other
// than the base URL's reduced to its text, since the text comes from
// Unleash and the links of a result must stay on the instance.
func (s *Source) ownLinks(text string) string {
	base, err := url.Parse(s.cfg.baseURL())
	if err != nil {
		return markdownLink.ReplaceAllString(text, "$1")
	}

	return markdownLink.ReplaceAllStringFunc(text, func(link string) string {
		parts := markdownLink.FindStringSubmatch(link)

		target, err := url.Parse(parts[2])
		if err == nil && target.Scheme == base.Scheme && strings.EqualFold(target.Host, base.Host) {
			return link
		}

		return parts[1]
	})
}

// flagEvidence returns the links to the pages of the flags keys names, at
// most maxEvidence of them, or to the project's page when keys is empty.
func (s *Source) flagEvidence(scope flags.Scope, keys []string) []source.Evidence {
	if len(keys) == 0 {
		return []source.Evidence{{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("Unleash flags in %s/%s", scope.Project, scope.Environment),
			URL:   s.flagURL(scope.Project, ""),
		}}
	}

	keys = keys[:min(len(keys), maxEvidence)]
	evidence := make([]source.Evidence, 0, len(keys))

	for _, key := range keys {
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("Unleash flag %s in %s/%s", key, scope.Project, scope.Environment),
			URL:   s.flagURL(scope.Project, key),
		})
	}

	return evidence
}
