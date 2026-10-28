package launchdarkly

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// auditLogPath is the audit log API.
const auditLogPath = "/api/v2/auditlog"

// auditPageSize is the most entries LaunchDarkly returns in one page of
// the audit log.
const auditPageSize = 20

// maxAuditPages bounds the pages one call reads. flags.MaxChanges entries
// fit in fewer; the bound only stops a service that keeps answering.
const maxAuditPages = flags.MaxChanges/auditPageSize + 3

// maxEvidence bounds the flag links one list_flag_changes result gives.
const maxEvidence = 10

// auditPage is one page of the audit log.
type auditPage struct {
	Items []auditEntry `json:"items"`
}

// auditEntry is the part of an audit log entry the source reads.
type auditEntry struct {
	ID               string   `json:"_id"`
	Date             int64    `json:"date"`
	Kind             string   `json:"kind"`
	Name             string   `json:"name"`
	Description      string   `json:"description"`
	ShortDescription string   `json:"shortDescription"`
	Comment          string   `json:"comment"`
	TitleVerb        string   `json:"titleVerb"`
	Accesses         []access `json:"accesses"`
	Member           *struct {
		Email     string `json:"email"`
		FirstName string `json:"firstName"`
		LastName  string `json:"lastName"`
	} `json:"member"`
	Token *struct {
		Name   string `json:"name"`
		Ending string `json:"ending"`
	} `json:"token"`
	App *struct {
		Name string `json:"name"`
	} `json:"app"`
	Subject *struct {
		Name string `json:"name"`
	} `json:"subject"`
	Target *struct {
		Resources []string `json:"resources"`
		Links     links    `json:"_links"`
	} `json:"target"`
	Links links `json:"_links"`
}

// access is one action an audit log entry records, on one resource.
type access struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// links are the related links of a LaunchDarkly resource, by relation.
// "site" is its page in the app.
type links map[string]struct {
	Href string `json:"href"`
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

	entries, more, err := s.auditLog(ctx, spec(scope, flag), w, flags.Limit(in.Limit))
	if err != nil {
		return source.Result{}, fmt.Errorf("read the audit log of %s/%s: %w", scope.Project, scope.Environment, err)
	}

	list := flags.NewChangeList(scope, flag, w)

	var keys []string

	for _, e := range entries {
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

// auditLog reads the audit log entries in w of the resources a resource
// specifier names, newest first, up to limit, and reports whether there
// are more. LaunchDarkly gives at most auditPageSize entries a page, so it
// pages back in time: each page ends one millisecond after the oldest
// entry of the last one, so entries that share that millisecond are read
// again and skipped by ID rather than lost.
func (s *Source) auditLog(ctx context.Context, resources string, w flags.Window, limit int) ([]auditEntry, bool, error) {
	start, end := w.Start.UnixMilli(), w.End.UnixMilli()
	before := end
	seen := map[string]bool{}
	// again is how many entries already read the next page returns again.
	again := 0

	var out []auditEntry

	for range maxAuditPages {
		if again >= auditPageSize {
			// A full page of one millisecond leaves no way to page further.
			return sortEntries(out), true, nil
		}

		want := min(auditPageSize, limit-len(out)+1+again)

		query := url.Values{
			"spec":   {resources},
			"after":  {strconv.FormatInt(start-1, 10)},
			"before": {strconv.FormatInt(before, 10)},
			"limit":  {strconv.Itoa(want)},
		}

		var page auditPage
		if err := s.client.get(ctx, auditLogPath, query, &page); err != nil {
			return nil, false, fmt.Errorf("read a page of the audit log: %w", err)
		}

		added := 0
		oldest := before

		for _, e := range page.Items {
			oldest = min(oldest, e.Date)

			if seen[e.ID] || e.Date < start || e.Date >= end {
				continue
			}

			if len(out) == limit {
				return sortEntries(out), true, nil
			}

			seen[e.ID] = true
			out = append(out, e)
			added++
		}

		switch {
		case len(page.Items) < want || oldest < start:
			return sortEntries(out), false, nil
		case added == 0:
			return sortEntries(out), true, nil
		}

		before = oldest + 1
		again = countAt(out, oldest)
	}

	return sortEntries(out), true, nil
}

// change returns the change an audit log entry records.
func (s *Source) change(e auditEntry, scope flags.Scope) flags.Change {
	key := e.flagKey()

	c := flags.Change{
		Time:    time.UnixMilli(e.Date).UTC(),
		Flag:    key,
		Name:    e.Name,
		Action:  e.TitleVerb,
		Actor:   e.actor(),
		Summary: strings.TrimSpace(e.Description),
		Comment: strings.TrimSpace(e.Comment),
		URL:     s.siteURL(e.site()),
	}

	if c.Summary == "" {
		c.Summary = strings.TrimSpace(e.ShortDescription)
	}

	if c.URL == "" && key != "" && keyPattern.MatchString(key) {
		c.URL = s.flagURL(scope, key)
	}

	return c
}

// flagKey returns the key of the flag the entry changed, from the first
// flag resource among its accesses and target, or nothing.
func (e auditEntry) flagKey() string {
	resources := make([]string, 0, len(e.Accesses))
	for _, a := range e.Accesses {
		resources = append(resources, a.Resource)
	}

	if e.Target != nil {
		resources = append(resources, e.Target.Resources...)
	}

	for _, r := range resources {
		if key := resourceFlag(r); key != "" {
			return key
		}
	}

	return ""
}

// actor returns who made the change: a member, an access token, an
// integration, or whatever subject LaunchDarkly names.
func (e auditEntry) actor() string {
	switch {
	case e.Member != nil && e.Member.Email != "":
		return e.Member.Email
	case e.Member != nil:
		return strings.TrimSpace(e.Member.FirstName + " " + e.Member.LastName)
	case e.Token != nil:
		return "access token " + e.Token.Name
	case e.App != nil:
		return "integration " + e.App.Name
	case e.Subject != nil:
		return e.Subject.Name
	default:
		return ""
	}
}

// site returns the app link of the entry's target, or of the entry.
func (e auditEntry) site() string {
	if e.Target != nil {
		if l, ok := e.Target.Links["site"]; ok && l.Href != "" {
			return l.Href
		}
	}

	return e.Links["site"].Href
}

// flagEvidence returns the links to the pages of the flags keys names, at
// most maxEvidence of them, or to the environment's flag list when keys is
// empty.
func (s *Source) flagEvidence(scope flags.Scope, keys []string) []source.Evidence {
	if len(keys) == 0 {
		return []source.Evidence{{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("LaunchDarkly flags in %s/%s", scope.Project, scope.Environment),
			URL:   s.flagURL(scope, ""),
		}}
	}

	keys = keys[:min(len(keys), maxEvidence)]
	evidence := make([]source.Evidence, 0, len(keys))

	for _, key := range keys {
		evidence = append(evidence, source.Evidence{
			Kind:  source.EvidenceDeploy,
			Title: fmt.Sprintf("LaunchDarkly flag %s in %s/%s", key, scope.Project, scope.Environment),
			URL:   s.flagURL(scope, key),
		})
	}

	return evidence
}

// spec returns the resource specifier of the flags of scope, or of one
// flag. Every key in it has been checked against keyPattern, so none can
// widen it.
func spec(scope flags.Scope, flag string) string {
	if flag == "" {
		flag = "*"
	}

	return "proj/" + scope.Project + ":env/" + scope.Environment + ":flag/" + flag
}

// resourceFlag returns the flag key of a resource specifier such as
// proj/shop:env/production:flag/new-checkout, or nothing when it names no
// single flag.
func resourceFlag(resource string) string {
	for part := range strings.SplitSeq(resource, ":") {
		key, ok := strings.CutPrefix(part, "flag/")
		if !ok {
			continue
		}

		key, _, _ = strings.Cut(key, ";")
		if key == "*" {
			return ""
		}

		return key
	}

	return ""
}

// countAt returns how many entries are of the millisecond at.
func countAt(entries []auditEntry, at int64) int {
	n := 0

	for _, e := range entries {
		if e.Date == at {
			n++
		}
	}

	return n
}

// sortEntries sorts entries newest first, keeping LaunchDarkly's order
// for entries of the same millisecond.
func sortEntries(entries []auditEntry) []auditEntry {
	slices.SortStableFunc(entries, func(a, b auditEntry) int {
		switch {
		case a.Date > b.Date:
			return -1
		case a.Date < b.Date:
			return 1
		default:
			return 0
		}
	})

	return entries
}
