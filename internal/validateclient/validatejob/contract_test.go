package validatejob

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/validate"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// TestLimitsMatchTheValidator checks the copies of the validator's limits
// and failure kinds, which this package keeps so it does not link the
// validator's tools.
func TestLimitsMatchTheValidator(t *testing.T) {
	t.Parallel()

	require.Equal(t, validate.MaxConcurrent, maxConcurrent)
	require.Equal(t, validate.SlotWait, slotWait)
	require.Equal(t, validate.Timeout, validationTimeout)
	require.Equal(t, validate.MaxFiles, validateclient.MaxFiles)
	require.Equal(t, validate.MaxRequestBytes, validateclient.MaxRequestBytes)
	require.Equal(t, validate.KindBadRequest, kindBadRequest)
	require.Equal(t, validate.KindTimeout, kindTimeout)
	require.Equal(t, validate.KindBusy, kindBusy)
}

// TestFailureShapeMatchesTheValidator checks that the failure message of
// the job command decodes into this package's copy of it.
func TestFailureShapeMatchesTheValidator(t *testing.T) {
	t.Parallel()

	raw, err := json.Marshal(validate.JobError{Error: "boom", Kind: validate.KindTimeout})
	require.NoError(t, err)

	var got jobFailure

	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, jobFailure{Error: "boom", Kind: kindTimeout}, got)
}
