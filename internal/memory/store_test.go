package memory

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/redact"
)

func TestCheckText(t *testing.T) {
	t.Parallel()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key"), Custom: []redact.Rule{{Name: "ticket", Pattern: `TICKET-[0-9]+`}}})
	require.NoError(t, err)

	long := strings.Repeat("a", MaxSummaryChars+1)

	tests := []struct {
		name     string
		text     string
		stored   string
		max      int
		required bool
		want     string
		// wantField is the field of the *InvalidError, empty for none.
		wantField string
	}{
		{name: "new text is trimmed", text: "  Pool was full.\n", stored: "Old.", max: 100, required: true, want: "Pool was full."},
		{name: "text equal to the stored text is kept as stored", text: "Same.", stored: " Same. ", max: 100, required: true, want: " Same. "},
		{name: "stored text over the limit can be kept", text: long, stored: long, max: MaxSummaryChars, required: true, want: long},
		{name: "new text over the limit is refused", text: long, stored: "Old.", max: MaxSummaryChars, required: true, wantField: "summary"},
		{name: "the limit counts characters, not bytes", text: strings.Repeat("é", 10), stored: "Old.", max: 10, required: true, want: strings.Repeat("é", 10)},
		{name: "an empty required text is refused", text: "  ", stored: "Old.", max: 100, required: true, wantField: "summary"},
		{name: "an empty optional text clears it", text: "", stored: "Old.", max: 100, want: ""},
		{name: "control characters are refused", text: "a\x00b", stored: "Old.", max: 100, required: true, wantField: "summary"},
		{name: "new lines and tabs are allowed", text: "a\n\tb", stored: "Old.", max: 100, required: true, want: "a\n\tb"},
		{name: "the limit applies to the redacted text", text: strings.Repeat("a", 5) + " TICKET-12", stored: "Old.", max: 20, required: true, wantField: "summary"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := &Store{redactor: redactor}

			got, err := s.checkText("summary", tt.text, tt.stored, tt.max, tt.required)

			if tt.wantField != "" {
				invalid, ok := errors.AsType[*InvalidError](err)
				require.True(t, ok, "want an *InvalidError, got %v", err)
				require.Equal(t, tt.wantField, invalid.Field)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestCheckTextRedacts(t *testing.T) {
	t.Parallel()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key"), Custom: []redact.Rule{{Name: "ticket", Pattern: `TICKET-[0-9]+`}}})
	require.NoError(t, err)

	got, err := (&Store{redactor: redactor}).checkText("summary", "See TICKET-4711 for the pool.", "Old.", 400, true)
	require.NoError(t, err)
	require.NotContains(t, got, "TICKET-4711")
	require.Contains(t, got, "for the pool.")
}

func TestUpdateAction(t *testing.T) {
	t.Parallel()

	base := Entry{Summary: "Summary.", Cause: "Cause."}

	tests := []struct {
		name  string
		after func(Entry) Entry
		want  string
	}{
		{name: "a new summary", after: func(e Entry) Entry { e.Summary = "New."; return e }, want: auditUpdated},
		{name: "a new cause", after: func(e Entry) Entry { e.Cause = "New."; return e }, want: auditUpdated},
		{name: "a new summary and disabled", after: func(e Entry) Entry { e.Summary, e.Disabled = "New.", true; return e }, want: auditUpdated},
		{name: "disabled only", after: func(e Entry) Entry { e.Disabled = true; return e }, want: auditDisabled},
		{name: "enabled only", after: func(e Entry) Entry { return e }, want: auditEnabled},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			before := base
			if tt.want == auditEnabled {
				before.Disabled = true
			}

			require.Equal(t, tt.want, updateAction(before, tt.after(before)))
		})
	}
}
