package validateclient

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/validate"
)

// These tests keep the client's copy of the wire types in step with the
// server's, which the client cannot import without linking the validator's
// tools into the main binary.

func TestWireRequestMatchesServer(t *testing.T) {
	t.Parallel()

	req := Request{Format: FormatTerraform, Dir: "datadog/monitors", Files: []File{{Path: "datadog/monitors/main.tf", Content: "resource {}\n"}}}

	body, err := json.Marshal(req)
	require.NoError(t, err)

	got, err := validate.DecodeRequest(bytes.NewReader(body))
	require.NoError(t, err, "the server refuses unknown fields")

	back, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(back))
}

func TestWireResultMatchesServer(t *testing.T) {
	t.Parallel()

	res := validate.Result{
		OK:         false,
		Errors:     []validate.Problem{{Path: "rules/a.yaml", Line: 4, Message: "bad"}},
		NotChecked: []validate.Problem{{Path: "g.yaml", Message: "loki"}},
		Truncated:  true,
	}

	body, err := json.Marshal(res)
	require.NoError(t, err)

	var got Result

	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	require.NoError(t, dec.Decode(&got), "the client knows every field the server sends")

	back, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, string(body), string(back))
}

func TestWireConstantsMatchServer(t *testing.T) {
	t.Parallel()

	require.Equal(t, string(validate.FormatPrometheusRules), string(FormatPrometheusRules))
	require.Equal(t, string(validate.FormatTerraform), string(FormatTerraform))
	require.Equal(t, string(validate.FormatGrafanaAlertFile), string(FormatGrafanaAlertFile))
	require.Equal(t, validate.MaxFiles, MaxFiles)
	require.Equal(t, validate.MaxRequestBytes, MaxRequestBytes)
}
