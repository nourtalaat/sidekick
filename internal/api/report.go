package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// reportOf is the API's view of a stored report. Every list is an array,
// never null, and an evidence link that is not an http or https address is
// left out, since the page opens links.
func reportOf(raw json.RawMessage) (apigen.Report, error) {
	var rep report.Report
	if err := json.Unmarshal(raw, &rep); err != nil {
		return apigen.Report{}, fmt.Errorf("decode: %w", err)
	}

	out := apigen.Report{
		Verdict:    apigen.Verdict(rep.Verdict),
		Confidence: apigen.Confidence(rep.Confidence),
		Reason:     rep.Reason,
		Summary:    rep.Summary,
		Cause:      nonEmpty(&rep.Cause),
		Impact:     nonEmpty(&rep.Impact),
		Findings:   make([]apigen.Finding, 0, len(rep.Findings)),
		Checked:    make([]apigen.SourceCheck, 0, len(rep.Checked)),
		StopReason: apigen.ReportStopReason(rep.StopReason),
		StopDetail: nonEmpty(&rep.StopDetail),
	}

	for _, f := range rep.Findings {
		out.Findings = append(out.Findings, apigen.Finding{Id: f.ID, Claim: f.Claim, Evidence: evidenceOf(f.Evidence)})
	}

	for _, c := range rep.Checked {
		out.Checked = append(out.Checked, apigen.SourceCheck{
			Source: c.Source, Status: apigen.SourceCheckStatus(c.Status), ToolCalls: c.ToolCalls, Error: nonEmpty(&c.Error),
		})
	}

	if len(rep.Fixes) > 0 {
		fixes := make([]apigen.Fix, 0, len(rep.Fixes))
		for _, f := range rep.Fixes {
			fixes = append(fixes, apigen.Fix{Id: f.ID, Title: f.Title, Detail: f.Detail, ActionProposalId: nonEmpty(&f.ActionProposalID)})
		}

		out.Fixes = &fixes
	}

	if rep.FalsePositive != nil {
		out.FalsePositive = falsePositiveOf(*rep.FalsePositive)
	}

	if len(rep.FollowUps) > 0 {
		followUps := make([]apigen.FollowUp, 0, len(rep.FollowUps))
		for _, f := range rep.FollowUps {
			followUps = append(followUps, followUpOf(f))
		}

		out.FollowUps = &followUps
	}

	return out, nil
}

// falsePositiveOf is the API's view of the details of a false positive.
func falsePositiveOf(d report.FalsePositiveDetail) *apigen.FalsePositiveDetail {
	out := apigen.FalsePositiveDetail{WhyFired: d.WhyFired, Prevention: make([]apigen.Prevention, 0, len(d.Prevention))}

	for _, p := range d.Prevention {
		out.Prevention = append(out.Prevention, apigen.Prevention{Kind: string(p.Kind), Detail: p.Detail})
	}

	return &out
}

// followUpOf is the API's view of one follow-up run.
func followUpOf(f report.FollowUp) apigen.FollowUp {
	out := apigen.FollowUp{
		Run:       f.Run,
		Questions: make([]apigen.FollowUpQuestion, 0, len(f.Questions)),
		Answer:    f.Answer,
		Answered:  f.Answered,
		At:        f.At,
	}

	for _, q := range f.Questions {
		out.Questions = append(out.Questions, apigen.FollowUpQuestion{Author: nonEmpty(&q.Author), Text: q.Text})
	}

	if len(f.Evidence) > 0 {
		evidence := evidenceOf(f.Evidence)
		out.Evidence = &evidence
	}

	if f.PreviousVerdict != "" {
		out.PreviousVerdict = new(apigen.Verdict(f.PreviousVerdict))
	}

	return out
}

// evidenceOf is the API's view of evidence links.
func evidenceOf(evidence []source.Evidence) []apigen.Evidence {
	out := make([]apigen.Evidence, 0, len(evidence))

	for _, e := range evidence {
		view := apigen.Evidence{Kind: string(e.Kind), Title: e.Title, ToolCallId: nonEmpty(&e.ToolCallID)}

		if isWebURL(e.URL) {
			view.Url = &e.URL
		}

		if !e.At.IsZero() {
			view.At = &e.At
		}

		out = append(out, view)
	}

	return out
}

// isWebURL reports whether raw is an absolute http or https address,
// which is the only kind of link the page opens.
func isWebURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return false
	}

	return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
}
