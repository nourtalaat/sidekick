package main

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// benchConfig is the config the benchmark workflow runs with.
var benchConfig = filepath.Join("..", "..", "bench", "sidekick.yaml")

func TestBenchWorkflowConfigIsValid(t *testing.T) {
	t.Parallel()

	var out, stderr bytes.Buffer

	root := newTestRootCmd(newConfigCmd(fakeRegistries()))
	root.SetOut(&out)
	root.SetErr(&stderr)
	root.SetArgs([]string{"config", "validate", "-f", benchConfig})

	require.NoError(t, root.Execute(), stderr.String())
	require.Equal(t, benchConfig+" is valid\n", out.String())
}
