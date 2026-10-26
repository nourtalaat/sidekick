package investigator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestUniqueKey(t *testing.T) {
	t.Parallel()

	groupID := uuid.MustParse("0192d3a4-0000-7000-8000-000000000001")

	require.Equal(t, "investigate:0192d3a4-0000-7000-8000-000000000001", UniqueKey(groupID))
}

func TestStoredJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value any
		want  string
		// decoded is a pointer to a zero value to decode want into.
		decoded any
	}{
		{
			name:    "payload",
			value:   Payload{InvestigationID: uuid.MustParse("0192d3a4-0000-7000-8000-000000000002"), GroupID: uuid.MustParse("0192d3a4-0000-7000-8000-000000000001")},
			want:    `{"investigation_id": "0192d3a4-0000-7000-8000-000000000002", "group_id": "0192d3a4-0000-7000-8000-000000000001"}`,
			decoded: &Payload{},
		},
		{
			name:    "budget",
			value:   Budget{MaxTokens: 400_000, MaxToolCalls: 30, MaxWallTime: 90 * time.Second, MaxCostMicros: 750_000},
			want:    `{"max_tokens": 400000, "max_tool_calls": 30, "max_wall_time_ms": 90000, "max_cost_micros": 750000}`,
			decoded: &Budget{},
		},
		{
			name:    "budget stored before token and cost limits",
			value:   Budget{MaxToolCalls: 30, MaxWallTime: 90 * time.Second},
			want:    `{"max_tool_calls": 30, "max_wall_time_ms": 90000}`,
			decoded: &Budget{},
		},
		{
			name:    "budget without limits",
			value:   Budget{},
			want:    `{"max_tool_calls": 0, "max_wall_time_ms": 0}`,
			decoded: &Budget{},
		},
		{
			name: "connectors",
			value: Connectors{
				model.RoleInvestigate: {Connector: "claude", Model: "claude-opus-5-5"},
				model.RoleSummarize:   {Connector: "local"},
			},
			want:    `{"investigate": {"connector": "claude", "model": "claude-opus-5-5"}, "summarize": {"connector": "local"}}`,
			decoded: &Connectors{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := json.Marshal(tt.value)
			require.NoError(t, err)
			require.JSONEq(t, tt.want, string(got))

			require.NoError(t, json.Unmarshal(got, tt.decoded))
			require.Equal(t, tt.value, derefAny(tt.decoded))
		})
	}
}

func TestBudgetRejectsBadJSON(t *testing.T) {
	t.Parallel()

	var b Budget
	require.Error(t, json.Unmarshal([]byte(`{"max_tool_calls": "many"}`), &b))
}

// derefAny returns the value a pointer of the test table points to.
func derefAny(p any) any {
	switch v := p.(type) {
	case *Payload:
		return *v
	case *Budget:
		return *v
	case *Connectors:
		return *v
	default:
		return nil
	}
}
