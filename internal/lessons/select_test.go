package lessons

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// defaultLimits are the limits of the default config: 10 lessons, 1,500
// tokens, and 30% of both for global lessons, so 3 places and 450 tokens.
var defaultLimits = Limits{MaxPerPrompt: 10, MaxTokens: 1500, GlobalPercent: 30}

// idOf returns the ID of lesson number n.
func idOf(n int) uuid.UUID {
	return uuid.MustParse(fmt.Sprintf("00000000-0000-0000-0000-%012d", n))
}

// scoped and global are scopes for the tests.
var (
	scoped = Scope{Service: "s"}
	global = Scope{}
)

// lessonOf returns lesson number n with the scope, whose text is as long as
// it takes for the lesson to count as tokens.
func lessonOf(n int, scope Scope, tokens int) Lesson {
	size := (tokens - overheadTokens) * bytesPerToken
	text := strings.Repeat("a", size-len(scope.Describe()))

	return Lesson{ID: idOf(n), Text: text, Scope: scope}
}

// lessonsOf returns count lessons numbered from first.
func lessonsOf(first, count int, scope Scope, tokens int) []Lesson {
	out := make([]Lesson, count)
	for i := range out {
		out[i] = lessonOf(first+i, scope, tokens)
	}

	return out
}

// idsOf returns the IDs of lessons.
func idsOf(lessons []Lesson) []uuid.UUID {
	out := make([]uuid.UUID, len(lessons))
	for i, l := range lessons {
		out[i] = l.ID
	}

	return out
}

func TestChoose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		candidates []Lesson
		total      int
		limits     Limits
		// wantUsed are the numbers of the lessons used, and wantLeftOut
		// the reasons by lesson number, in rank order.
		wantUsed     []int
		wantLeftOut  map[int]string
		wantLeftTot  int
		wantListed   int
		wantTotalSet int
	}{
		{
			name:       "everything fits",
			candidates: lessonsOf(1, 3, scoped, 40),
			limits:     defaultLimits,
			wantUsed:   []int{1, 2, 3},
		},
		{
			name:        "no more than the most lessons",
			candidates:  lessonsOf(1, 12, scoped, 40),
			limits:      defaultLimits,
			wantUsed:    []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			wantLeftOut: map[int]string{11: ReasonMaxPerPrompt, 12: ReasonMaxPerPrompt},
			wantLeftTot: 2,
			wantListed:  2,
		},
		{
			name:        "a lesson that takes the tokens over the limit is left out",
			candidates:  lessonsOf(1, 3, scoped, 40),
			limits:      Limits{MaxPerPrompt: 10, MaxTokens: 100, GlobalPercent: 30},
			wantUsed:    []int{1, 2},
			wantLeftOut: map[int]string{3: ReasonMaxTokens},
			wantLeftTot: 1,
			wantListed:  1,
		},
		{
			name:        "a shorter lesson further down can still fit",
			candidates:  []Lesson{lessonOf(1, scoped, 40), lessonOf(2, scoped, 40), lessonOf(3, scoped, 40), lessonOf(4, scoped, 30)},
			limits:      Limits{MaxPerPrompt: 10, MaxTokens: 110, GlobalPercent: 30},
			wantUsed:    []int{1, 2, 4},
			wantLeftOut: map[int]string{3: ReasonMaxTokens},
			wantLeftTot: 1,
			wantListed:  1,
		},
		{
			name:       "exactly the token limit fits",
			candidates: lessonsOf(1, 2, scoped, 40),
			limits:     Limits{MaxPerPrompt: 10, MaxTokens: 80, GlobalPercent: 30},
			wantUsed:   []int{1, 2},
		},
		{
			name:        "global lessons get a third of the places",
			candidates:  lessonsOf(1, 5, global, 40),
			limits:      defaultLimits,
			wantUsed:    []int{1, 2, 3},
			wantLeftOut: map[int]string{4: ReasonGlobalShare, 5: ReasonGlobalShare},
			wantLeftTot: 2,
			wantListed:  2,
		},
		{
			name:        "global lessons get 30 percent of the tokens",
			candidates:  lessonsOf(1, 3, global, 200),
			limits:      defaultLimits,
			wantUsed:    []int{1, 2},
			wantLeftOut: map[int]string{3: ReasonGlobalShare},
			wantLeftTot: 1,
			wantListed:  1,
		},
		{
			name:        "a global share of zero leaves out every global lesson",
			candidates:  append(lessonsOf(1, 1, scoped, 40), lessonsOf(2, 2, global, 40)...),
			limits:      Limits{MaxPerPrompt: 10, MaxTokens: 1500, GlobalPercent: 0},
			wantUsed:    []int{1},
			wantLeftOut: map[int]string{2: ReasonGlobalShare, 3: ReasonGlobalShare},
			wantLeftTot: 2,
			wantListed:  2,
		},
		{
			name:       "a global share of 100 percent is no limit",
			candidates: lessonsOf(1, 5, global, 40),
			limits:     Limits{MaxPerPrompt: 10, MaxTokens: 1500, GlobalPercent: 100},
			wantUsed:   []int{1, 2, 3, 4, 5},
		},
		{
			name:       "scoped lessons are not held back by the global share",
			candidates: append(lessonsOf(1, 10, scoped, 40), lessonsOf(11, 2, global, 40)...),
			limits:     defaultLimits,
			wantUsed:   []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			wantLeftOut: map[int]string{
				11: ReasonMaxPerPrompt, 12: ReasonMaxPerPrompt,
			},
			wantLeftTot: 2,
			wantListed:  2,
		},
		{
			name:        "scoped lessons and global ones share the places",
			candidates:  append(lessonsOf(1, 8, scoped, 40), lessonsOf(9, 3, global, 40)...),
			limits:      defaultLimits,
			wantUsed:    []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			wantLeftOut: map[int]string{11: ReasonMaxPerPrompt},
			wantLeftTot: 1,
			wantListed:  1,
		},
		{
			name:        "the reason is the first limit a lesson meets",
			candidates:  lessonsOf(1, 3, global, 400),
			limits:      Limits{MaxPerPrompt: 1, MaxTokens: 100, GlobalPercent: 30},
			wantUsed:    nil,
			wantLeftOut: map[int]string{1: ReasonMaxTokens, 2: ReasonMaxTokens, 3: ReasonMaxTokens},
			wantLeftTot: 3,
			wantListed:  3,
		},
		{
			name:         "lessons that were not read count as left out",
			candidates:   lessonsOf(1, 100, scoped, 40),
			total:        130,
			limits:       defaultLimits,
			wantUsed:     []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			wantLeftTot:  120,
			wantListed:   50,
			wantTotalSet: 130,
		},
		{
			name:         "at most 50 left-out lessons are listed",
			candidates:   lessonsOf(1, 70, scoped, 40),
			limits:       defaultLimits,
			wantUsed:     []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
			wantLeftTot:  60,
			wantListed:   50,
			wantTotalSet: 70,
		},
		{
			name:   "no lessons apply",
			limits: defaultLimits,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Choose(tt.candidates, max(tt.total, len(tt.candidates)), tt.limits)

			var wantUsed []Lesson
			for _, n := range tt.wantUsed {
				wantUsed = append(wantUsed, tt.candidates[n-1])
			}

			require.Equal(t, idsOf(wantUsed), idsOf(got.Used))
			require.Equal(t, tt.wantLeftTot, got.LeftOutTotal)
			require.Len(t, got.LeftOut, tt.wantListed)

			for _, l := range got.LeftOut {
				n := numberOf(t, l.ID)
				if want, ok := tt.wantLeftOut[n]; ok {
					require.Equal(t, want, l.Reason, "lesson %d", n)
				}
			}

			if tt.wantTotalSet != 0 {
				require.Equal(t, tt.wantTotalSet, got.Candidates)
			}
		})
	}
}

// numberOf returns the number idOf made id from.
func numberOf(t *testing.T, id uuid.UUID) int {
	t.Helper()

	var n int

	_, err := fmt.Sscanf(id.String(), "00000000-0000-0000-0000-%012d", &n)
	require.NoError(t, err)

	return n
}

func TestChooseKeepsRankOrder(t *testing.T) {
	t.Parallel()

	candidates := []Lesson{
		lessonOf(1, scoped, 40), lessonOf(2, global, 40), lessonOf(3, scoped, 40), lessonOf(4, global, 40),
	}

	got := Choose(candidates, len(candidates), defaultLimits)
	require.Equal(t, idsOf(candidates), idsOf(got.Used))
}

func TestTokens(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		lesson Lesson
		want   int
	}{
		{name: "text and scope divided by three, plus the overhead", lesson: Lesson{Text: strings.Repeat("a", 51), Scope: scoped}, want: 40},
		{name: "a partial token counts as one", lesson: Lesson{Text: strings.Repeat("a", 52), Scope: scoped}, want: 41},
		{name: "a global lesson counts its description", lesson: Lesson{Text: strings.Repeat("a", 41)}, want: 40},
		{name: "text of the most characters", lesson: Lesson{Text: strings.Repeat("a", MaxTextChars), Scope: Scope{Service: "checkout-api", Alert: "CheckoutLatencyP99"}}, want: 20 + (400+len("service checkout-api, alert CheckoutLatencyP99")+2)/3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, Tokens(tt.lesson))
		})
	}
}

func TestLimitsOf(t *testing.T) {
	t.Parallel()

	tests := []struct {
		share float64
		want  int
	}{
		{share: 0.3, want: 30},
		{share: 0, want: 0},
		{share: 1, want: 100},
		{share: 0.07, want: 7},
		{share: 0.333, want: 33},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.share), func(t *testing.T) {
			t.Parallel()

			require.Equal(t, Limits{MaxPerPrompt: 10, MaxTokens: 1500, GlobalPercent: tt.want}, LimitsOf(10, 1500, tt.share))
		})
	}
}

func TestScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		scope        Scope
		wantGlobal   bool
		wantDescribe string
	}{
		{name: "no field is global", scope: Scope{}, wantGlobal: true, wantDescribe: "every investigation"},
		{name: "one field", scope: Scope{Route: "payments"}, wantDescribe: "route payments"},
		{name: "every field, in a fixed order", scope: Scope{Strategy: "t", Route: "r", Alert: "a", Service: "s"}, wantDescribe: "service s, alert a, route r, strategy t"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.wantGlobal, tt.scope.Global())
			require.Equal(t, tt.wantDescribe, tt.scope.Describe())
		})
	}
}

func TestStepRecord(t *testing.T) {
	t.Parallel()

	sel := Selection{
		Used:         []Lesson{{ID: idOf(1)}, {ID: idOf(2)}},
		LeftOut:      []LeftOut{{ID: idOf(3), Reason: ReasonGlobalShare, Scope: Scope{}}, {ID: idOf(4), Reason: ReasonMaxTokens, Scope: Scope{Service: "s"}}},
		LeftOutTotal: 5,
		Candidates:   7,
	}

	raw, err := sel.StepRecord()
	require.NoError(t, err)
	require.JSONEq(t, `{
		"used": ["00000000-0000-0000-0000-000000000001", "00000000-0000-0000-0000-000000000002"],
		"leftOut": [
			{"id": "00000000-0000-0000-0000-000000000003", "reason": "global_share", "scope": {}},
			{"id": "00000000-0000-0000-0000-000000000004", "reason": "max_tokens", "scope": {"service": "s"}}
		],
		"leftOutTotal": 5,
		"candidates": 7
	}`, string(raw))

	empty, err := Selection{}.StepRecord()
	require.NoError(t, err)
	require.JSONEq(t, `{"used": [], "leftOut": [], "leftOutTotal": 0, "candidates": 0}`, string(empty))

	var back StepRecord
	require.NoError(t, json.Unmarshal(raw, &back))
	require.Equal(t, []uuid.UUID{idOf(1), idOf(2)}, back.Used)
}
