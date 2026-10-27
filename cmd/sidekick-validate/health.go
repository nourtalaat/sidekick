package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/validate"
)

// healthTimeout bounds one health check.
const healthTimeout = 2 * time.Second

// newHealthCmd builds `sidekick-validate health`, which the chart's probes
// run inside the container: the sidecar listens on loopback, which the
// kubelet's HTTP probes cannot reach.
func newHealthCmd() *cobra.Command {
	var addr string

	cmd := &cobra.Command{
		Use:   "health",
		Short: "Check that the validator on --addr answers GET /healthz",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return checkHealth(cmd.Context(), addr)
		},
	}

	cmd.Flags().StringVar(&addr, "addr", validate.ListenAddr, "address the validator listens on")

	return cmd
}

// checkHealth calls GET /healthz on addr and fails unless it answers 200.
func checkHealth(ctx context.Context, addr string) error {
	ctx, cancel := context.WithTimeout(ctx, healthTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		return fmt.Errorf("make the health request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("check health: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("check health: GET /healthz answered %s", resp.Status)
	}

	return nil
}
