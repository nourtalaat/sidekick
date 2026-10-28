package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/model"
)

func TestInvestigationCursor(t *testing.T) {
	t.Parallel()

	c := investigationCursor{createdAt: time.Date(2026, time.October, 26, 14, 0, 1, 123456000, time.UTC), id: uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")}

	got, err := parseInvestigationCursor(c.String())
	require.NoError(t, err)
	require.True(t, c.createdAt.Equal(got.createdAt), "the time keeps its microseconds")
	require.Equal(t, c.id, got.id)

	tests := []struct {
		name   string
		cursor string
	}{
		{name: "not base64", cursor: "!!"},
		{name: "no separator", cursor: "bm9wZQ"},
		{name: "a time that is not a number", cursor: "eC4wMTkyZDNhNC0wMDAwLTcwMDAtODAwMC0wMDAwMDAwMDAwYzE"},
		{name: "an ID that is not a UUID", cursor: "MTIzLnV1aWQ"},
		{name: "empty", cursor: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := parseInvestigationCursor(tt.cursor)
			require.ErrorIs(t, err, errBadInvestigationCursor)
		})
	}
}

func TestFeedbackSummaryOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		correct, wrong int64
		want           apigen.FeedbackSummarySummary
	}{
		{name: "nobody rated", want: apigen.None},
		{name: "only correct", correct: 3, want: apigen.Correct},
		{name: "only wrong", wrong: 1, want: apigen.Wrong},
		{name: "both", correct: 2, wrong: 1, want: apigen.Mixed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := feedbackSummaryOf(tt.correct, tt.wrong)
			require.Equal(t, tt.want, got.Summary)
			require.Equal(t, int(tt.correct), got.Correct)
			require.Equal(t, int(tt.wrong), got.Wrong)
		})
	}
}

func TestIsWebURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url  string
		want bool
	}{
		{url: "https://example.com/a?b=c", want: true},
		{url: "http://example.com", want: true},
		{url: "HTTPS://EXAMPLE.COM", want: true},
		{url: "javascript:alert(1)"},
		{url: "data:text/html,<script>alert(1)</script>"},
		{url: "ftp://example.com/file"},
		{url: "//example.com/path"},
		{url: "/relative/path"},
		{url: "https://"},
		{url: ""},
		{url: "http://exa mple.com/%zz"},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, isWebURL(tt.url))
		})
	}
}

func TestReportOf(t *testing.T) {
	t.Parallel()

	t.Run("lists are never null", func(t *testing.T) {
		t.Parallel()

		rep, err := reportOf(json.RawMessage(`{"verdict": "inconclusive", "confidence": "low", "reason": "r", "findings": null, "checked": null, "stopReason": "error"}`))
		require.NoError(t, err)

		raw, err := json.Marshal(rep)
		require.NoError(t, err)
		require.JSONEq(t, `{"verdict": "inconclusive", "confidence": "low", "reason": "r", "summary": "", "findings": [], "checked": [], "stopReason": "error"}`, string(raw))
	})

	t.Run("links that are not web links are dropped", func(t *testing.T) {
		t.Parallel()

		rep, err := reportOf(json.RawMessage(`{"verdict": "true_positive", "confidence": "high", "reason": "r", "summary": "s", "stopReason": "submitted",
			"findings": [{"id": "f1", "claim": "c", "evidence": [
				{"kind": "metric", "title": "ok", "url": "https://example.com"},
				{"kind": "other", "title": "bad", "url": "javascript:alert(1)"}]}],
			"followUps": [{"run": 2, "questions": [], "answer": "a", "answered": true, "at": "2026-10-26T15:00:00Z",
				"evidence": [{"kind": "other", "title": "bad", "url": "data:text/html,x"}]}]}`))
		require.NoError(t, err)

		evidence := rep.Findings[0].Evidence
		require.Len(t, evidence, 2)
		require.Equal(t, "https://example.com", *evidence[0].Url)
		require.Nil(t, evidence[1].Url)

		require.Nil(t, (*(*rep.FollowUps)[0].Evidence)[0].Url)
	})

	t.Run("a report that is not JSON", func(t *testing.T) {
		t.Parallel()

		_, err := reportOf(json.RawMessage(`{`))
		require.Error(t, err)
	})
}

func TestPartsOf(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", maxPartBytes)

	parts := partsOf([]model.Part{
		model.Text{Text: "hello"},
		model.Reasoning{Provider: "anthropic", Text: "thinking", Signature: "secret"},
		model.Reasoning{Provider: "anthropic", Signature: "only a signature", Redacted: []byte("opaque")},
		model.ToolCall{ID: "c1", Name: "search", Input: json.RawMessage(`{"q": "x"}`)},
		model.ToolResult{CallID: "c1", Content: long, IsError: true},
		model.Text{Text: long},
		model.ToolCall{ID: "c2", Name: "search", Input: json.RawMessage(`{"q": "` + strings.Repeat("a", maxPartBytes) + `"}`)},
	})

	require.Len(t, parts, 6, "a reasoning part with no text is left out")

	require.Equal(t, apigen.StepPartTypeText, parts[0].Type)
	require.Nil(t, parts[0].Truncated)

	require.Equal(t, apigen.StepPartTypeReasoning, parts[1].Type)
	require.Equal(t, "thinking", *parts[1].Text)

	require.JSONEq(t, `{"q": "x"}`, string(parts[2].Input.(json.RawMessage)))

	require.True(t, *parts[3].Truncated)
	require.Len(t, *parts[3].Content, maxPartBytes, "cut at a character, not in the middle of one")
	require.True(t, *parts[3].IsError)

	require.True(t, *parts[4].Truncated)
	require.LessOrEqual(t, len(*parts[4].Text), maxPartBytes)

	require.True(t, *parts[5].Truncated)
	require.IsType(t, "", parts[5].Input, "arguments over the limit are sent as cut text")
}

func TestStepPartsDoNotLeakSignatures(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(partsOf([]model.Part{model.Reasoning{Text: "thinking", Signature: "secret-signature", Redacted: []byte("opaque")}}))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "secret-signature")
	require.NotContains(t, string(raw), "opaque")
}

func TestCutPart(t *testing.T) {
	t.Parallel()

	short, cut := cutPart("short")
	require.Equal(t, "short", short)
	require.False(t, cut)

	exact := strings.Repeat("a", maxPartBytes)
	got, cut := cutPart(exact)
	require.Equal(t, exact, got)
	require.False(t, cut)

	got, cut = cutPart(exact + "a")
	require.Len(t, got, maxPartBytes)
	require.True(t, cut)
}
