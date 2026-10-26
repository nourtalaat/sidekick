// Package report defines the structured result of an investigation.
// The model fills it by calling the submit_verdict tool.
package report

import (
	"errors"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Verdict is the investigation's conclusion (VR-1).
type Verdict string

// Verdicts.
const (
	TruePositive  Verdict = "true_positive"
	FalsePositive Verdict = "false_positive"
	Inconclusive  Verdict = "inconclusive"
	Duplicate     Verdict = "duplicate"
	KnownIssue    Verdict = "known_issue"
	Flapping      Verdict = "flapping"
)

// Confidence is low, medium or high (VR-2).
type Confidence string

// Confidence levels.
const (
	Low    Confidence = "low"
	Medium Confidence = "medium"
	High   Confidence = "high"
)

// confidenceOrder lists the confidence levels from lowest to highest.
var confidenceOrder = []Confidence{Low, Medium, High}

// Rank orders confidence levels from 1 (low) upwards; unknown values rank 0.
func (c Confidence) Rank() int {
	return slices.Index(confidenceOrder, c) + 1
}

// Finding is one claim with its evidence (VR-3).
type Finding struct {
	ID       string            `json:"id"`
	Claim    string            `json:"claim"`
	Evidence []source.Evidence `json:"evidence"`
}

// Hypothesis reports whether the finding has no evidence.
func (f Finding) Hypothesis() bool {
	return len(f.Evidence) == 0
}

// Fix is a proposed change, ordered by how fast it reduces impact (VR-4).
type Fix struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Detail string `json:"detail"`
	// ActionProposalID links the fix to an action proposal, when one was made.
	ActionProposalID string `json:"actionProposalId,omitempty"`
}

// PreventionKind is the kind of monitor change that stops a false positive (VR-5).
type PreventionKind string

// Prevention kinds.
const (
	PreventThreshold PreventionKind = "threshold"
	PreventDuration  PreventionKind = "duration"
	PreventFilter    PreventionKind = "filter"
	PreventExclusion PreventionKind = "exclusion"
	PreventDelete    PreventionKind = "delete_monitor"
	PreventOther     PreventionKind = "other"
)

// FalsePositiveDetail explains a false positive.
type FalsePositiveDetail struct {
	WhyFired   string       `json:"whyFired"`
	Prevention []Prevention `json:"prevention"`
}

// Prevention is one concrete monitor change.
type Prevention struct {
	Kind   PreventionKind `json:"kind"`
	Detail string         `json:"detail"`
}

// CheckStatus is the outcome of checking one source (VR-6).
type CheckStatus string

// Check statuses.
const (
	CheckOK          CheckStatus = "ok"
	CheckNoData      CheckStatus = "no_data"
	CheckFailed      CheckStatus = "failed"
	CheckUnavailable CheckStatus = "unavailable"
)

// Check records what was checked in one source.
type Check struct {
	Source    string      `json:"source"`
	Status    CheckStatus `json:"status"`
	ToolCalls int         `json:"toolCalls"`
	Error     string      `json:"error,omitempty"`
}

// StopReason says why the run ended.
type StopReason string

// Stop reasons.
const (
	StopSubmitted StopReason = "submitted"
	StopBudget    StopReason = "budget"
	StopCancelled StopReason = "cancelled"
	StopTriage    StopReason = "triage"
	StopError     StopReason = "error"
)

// Report is the investigation result.
type Report struct {
	Verdict       Verdict              `json:"verdict"`
	Confidence    Confidence           `json:"confidence"`
	Reason        string               `json:"reason"`
	Summary       string               `json:"summary"`
	Cause         string               `json:"cause,omitempty"`
	Impact        string               `json:"impact,omitempty"`
	Findings      []Finding            `json:"findings"`
	Fixes         []Fix                `json:"fixes,omitempty"`
	FalsePositive *FalsePositiveDetail `json:"falsePositive,omitempty"`
	// Checked and StopReason are filled by Sidekick, not by the model.
	Checked    []Check    `json:"checked"`
	StopReason StopReason `json:"stopReason"`
	StopDetail string     `json:"stopDetail,omitempty"`
}

// DefaultMinFalsePositiveConfidence is the lowest confidence a false_positive
// verdict may have when the rules do not set one.
const DefaultMinFalsePositiveConfidence = High

// Rules are the checks applied to a submitted verdict. They come from config.
// The zero value is the safe one: it requires DefaultMinFalsePositiveConfidence.
type Rules struct {
	// MinFalsePositiveConfidence guards against hiding real incidents (PRD
	// risk 1, VR-7). Empty or unknown values mean
	// DefaultMinFalsePositiveConfidence; lowering the bar takes an explicit
	// Low or Medium.
	MinFalsePositiveConfidence Confidence
}

// ErrInvalid wraps every validation failure; the message goes back to the model.
var ErrInvalid = errors.New("invalid report")

// Validate checks the report against VR-1 to VR-5 and the configured rules.
func (r Report) Validate(rules Rules) error {
	var errs []error
	switch r.Verdict {
	case TruePositive:
		if r.Cause == "" || r.Impact == "" || len(r.Fixes) == 0 {
			errs = append(errs, errors.New("true_positive needs cause, impact and at least one fix"))
		}
	case FalsePositive:
		if r.FalsePositive == nil || r.FalsePositive.WhyFired == "" || len(r.FalsePositive.Prevention) == 0 {
			errs = append(errs, errors.New("false_positive needs falsePositive.whyFired and at least one prevention"))
		}

		if minimum := rules.minFalsePositiveConfidence(); r.Confidence.Rank() < minimum.Rank() {
			errs = append(errs, fmt.Errorf("false_positive needs confidence %s or higher; gather more evidence or use inconclusive", minimum))
		}
	case Inconclusive, Duplicate, KnownIssue, Flapping:
	default:
		errs = append(errs, fmt.Errorf("unknown verdict %q", r.Verdict))
	}

	if r.Confidence.Rank() == 0 {
		errs = append(errs, fmt.Errorf("unknown confidence %q", r.Confidence))
	}

	if r.Reason == "" {
		errs = append(errs, errors.New("reason is required"))
	}

	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalid, err)
	}

	return nil
}

// minFalsePositiveConfidence returns the configured minimum, or the default
// when the configured value is empty or not a known level.
func (r Rules) minFalsePositiveConfidence() Confidence {
	if r.MinFalsePositiveConfidence.Rank() == 0 {
		return DefaultMinFalsePositiveConfidence
	}

	return r.MinFalsePositiveConfidence
}
