package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/intake/alertmanager"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

const (
	// testAlertTimeout bounds the delivery of the test alert.
	testAlertTimeout = 30 * time.Second
	// testAlertName is the alertname label of the test alert.
	testAlertName = "SidekickTestAlert"
	// testAlertGroup is the group key and receiver of the test alert, so it
	// is easy to find in Sidekick's data.
	testAlertGroup = "sidekick-test-alert"
	// fingerprintBytes is the size of the random fingerprint each test
	// alert gets, so Sidekick never takes it for a repeat of an earlier one.
	fingerprintBytes = 8
	// maxResponseBytes is the most of Sidekick's answer test-alert prints.
	maxResponseBytes = 64 << 10
)

// The environment variables the Helm chart sets from the pod's own name and
// namespace. When they are set, the test alert is about Sidekick's pod.
const (
	envPodName      = "POD_NAME"
	envPodNamespace = "POD_NAMESPACE"
)

var (
	errNoAlertmanagerWebhook = errors.New("no webhook endpoint uses the alertmanager adapter; add one under intake.webhooks, or name one with --webhook")
	errNotAlertmanager       = errors.New("test-alert sends an Alertmanager payload, so the endpoint must use the alertmanager adapter")
	errDeliveryRejected      = errors.New("the test alert was not accepted")
)

// testAlertOptions are the flags of test-alert.
type testAlertOptions struct {
	configPath string
	webhook    string
	baseURL    string
	channel    string
}

// newTestAlertCmd builds `sidekick test-alert`, which sends a sample
// Alertmanager alert to Sidekick's own webhook endpoint, authenticated as
// the endpoint's config says, and prints Sidekick's answer. With --channel
// it posts a sample alert into a Slack channel instead. regs are the
// plugin registries; the zero value means the global ones.
func newTestAlertCmd(regs config.Registries) *cobra.Command {
	var opts testAlertOptions

	cmd := &cobra.Command{
		Use:   "test-alert",
		Short: "Send a sample Alertmanager alert to Sidekick's own webhook endpoint",
		Long: `Send a sample Alertmanager alert to Sidekick's own webhook endpoint at
/hooks/<name>, with the endpoint's bearer token or HMAC signature from its
secret, and print Sidekick's answer. The alert then goes through routing
like any other: the route that matches it investigates it and shows the
investigation in its outputs, such as a Slack channel.

The endpoint is the one named with --webhook, or else the only endpoint
that uses the alertmanager adapter. The alert is sent to server.listen on
this machine, so run it inside Sidekick's pod
(kubectl exec deploy/sidekick -c sidekick -- sidekick test-alert), or
give Sidekick's address with --url.

With --channel it posts the sample alert into that Slack channel as the
bot instead, about a pod that the Kubernetes source can read (or a made-up
pod, which the message says), so that the listener of the channel picks it
up like an alert from a monitoring bot. The bot must be in the channel.
It prints which listener takes the alert, or why none does. This needs the
Slack bot token and no webhook endpoint.

The exit status is 1 when Sidekick or Slack does not accept the alert.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(cmd.Context(), opts.configPath, config.LoadOptions{Registries: regs})
			if err != nil {
				cmd.SilenceErrors = true

				if werr := printProblems(cmd.ErrOrStderr(), err); werr != nil {
					return errors.Join(errInvalidConfig, werr)
				}

				return errInvalidConfig
			}

			if opts.channel != "" {
				if opts.webhook != "" || opts.baseURL != "" {
					return errChannelWithWebhook
				}

				return sendSlackTestAlert(cmd.Context(), cmd.OutOrStdout(), cfg, regs, opts.channel, httpclient.New(), findSamplePod)
			}

			return sendTestAlert(cmd.Context(), cmd.OutOrStdout(), cfg, opts, httpclient.New(), time.Now())
		},
	}

	addConfigFlag(cmd, &opts.configPath)
	cmd.Flags().StringVar(&opts.webhook, "webhook", "", "the webhook endpoint to send to (default: the only one with the alertmanager adapter)")
	cmd.Flags().StringVar(&opts.baseURL, "url", "", "Sidekick's address, such as http://sidekick.monitoring:8080 (default: server.listen on this machine)")
	cmd.Flags().StringVar(&opts.channel, "channel", "", "post the sample alert into this Slack channel, such as '#alerts', as the bot, instead of sending it to a webhook")

	return cmd
}

// sendTestAlert sends the sample alert, sent at now, and prints the answer.
func sendTestAlert(ctx context.Context, out io.Writer, cfg *config.Config, opts testAlertOptions, hc *http.Client, now time.Time) error {
	name, err := testAlertWebhook(cfg, opts.webhook)
	if err != nil {
		return fmt.Errorf("choose the webhook endpoint: %w", err)
	}

	base := opts.baseURL
	if base == "" {
		base, err = localURL(cfg.Server.Listen)
		if err != nil {
			return fmt.Errorf("find Sidekick's address: %w", err)
		}
	}

	body, err := samplePayload(now)
	if err != nil {
		return fmt.Errorf("build the test alert: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, testAlertTimeout)
	defer cancel()

	target := strings.TrimSuffix(base, "/") + "/hooks/" + name

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if err := authenticate(ctx, req, cfg, cfg.Intake.Webhooks[name].Auth, body, now); err != nil {
		return fmt.Errorf("authenticate as webhook %s: %w", name, err)
	}

	if err := writeLine(out, "Sending a test alert to "+target); err != nil {
		return fmt.Errorf("print the target: %w", err)
	}

	return deliver(out, hc, req)
}

// deliver sends req and prints Sidekick's answer.
func deliver(out io.Writer, hc *http.Client, req *http.Request) error {
	resp, err := hc.Do(req)
	if err != nil {
		return fmt.Errorf("send the test alert: %w", err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("read the answer: %w", err)
	}

	if _, err := fmt.Fprintf(out, "Sidekick answered %s: %s\n", resp.Status, strings.TrimSpace(string(answer))); err != nil {
		return fmt.Errorf("print the answer: %w", err)
	}

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("%w: HTTP status %d", errDeliveryRejected, resp.StatusCode)
	}

	return writeLine(out, "The route that matches the alert now investigates it and shows it in its outputs, such as its Slack channel.")
}

// testAlertWebhook returns the endpoint to send to: named, or else the only
// one with the alertmanager adapter.
func testAlertWebhook(cfg *config.Config, named string) (string, error) {
	webhooks := cfg.Intake.Webhooks

	if named != "" {
		w, ok := webhooks[named]
		if !ok {
			return "", fmt.Errorf("intake.webhooks has no endpoint %q; it has %s", named, strings.Join(slices.Sorted(maps.Keys(webhooks)), ", "))
		}

		if w.Type != alertmanager.Adapter {
			return "", fmt.Errorf("endpoint %q uses the %s adapter: %w", named, w.Type, errNotAlertmanager)
		}

		return named, nil
	}

	var found []string

	for _, name := range slices.Sorted(maps.Keys(webhooks)) {
		if webhooks[name].Type == alertmanager.Adapter {
			found = append(found, name)
		}
	}

	switch len(found) {
	case 0:
		return "", errNoAlertmanagerWebhook
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf("several endpoints use the alertmanager adapter (%s); name one with --webhook", strings.Join(found, ", "))
	}
}

// authenticate adds the bearer token or the HMAC signature that auth asks
// for to req.
func authenticate(ctx context.Context, req *http.Request, cfg *config.Config, auth config.WebhookAuth, body []byte, now time.Time) error {
	s := secrets.NewDir(cfg.Secrets.Dir)

	switch {
	case auth.Bearer != nil:
		token, err := readSecret(ctx, s, "auth.bearer", *auth.Bearer)
		if err != nil {
			return fmt.Errorf("bearer token: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+token)
	case auth.HMAC != nil:
		key, err := readSecret(ctx, s, "auth.hmac", *auth.HMAC)
		if err != nil {
			return fmt.Errorf("HMAC key: %w", err)
		}

		ts := strconv.FormatInt(now.Unix(), 10)
		req.Header.Set(intake.HeaderTimestamp, ts)
		req.Header.Set(intake.HeaderSignature, intake.SignatureHeader([]byte(key), ts, body))
	}

	return nil
}

// readSecret reads the secret that field names, as the webhook endpoint
// reads it.
func readSecret(ctx context.Context, s registry.Secrets, field string, ref registry.SecretRef) (string, error) {
	value, err := secrets.ResolveText(ctx, s, ref)
	if err != nil {
		return "", fmt.Errorf("read %s, the Secret %s with key %s: %w", field, ref.Name, ref.Key, err)
	}

	return value, nil
}

// localURL turns a listen address such as ":8080" into the URL that
// reaches it on this machine.
func localURL(listen string) (string, error) {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "", fmt.Errorf("server.listen %q: %w", listen, err)
	}

	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}

	return "http://" + net.JoinHostPort(host, port), nil
}

// samplePayload is an Alertmanager webhook payload with one firing alert,
// started at now. A new random fingerprint each time keeps Sidekick from
// taking it for a repeat of an earlier test alert.
func samplePayload(now time.Time) ([]byte, error) {
	raw := make([]byte, fingerprintBytes)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("make a fingerprint: %w", err)
	}

	labels := map[string]string{
		"alertname": testAlertName,
		"severity":  "warning",
		"service":   "sidekick",
	}

	if ns := os.Getenv(envPodNamespace); ns != "" {
		labels["namespace"] = ns
	}

	if pod := os.Getenv(envPodName); pod != "" {
		labels["pod"] = pod
	}

	payload := map[string]any{
		"version":  "4",
		"groupKey": testAlertGroup,
		"receiver": testAlertGroup,
		"status":   "firing",
		"alerts": []map[string]any{{
			"status": "firing",
			"labels": labels,
			"annotations": map[string]string{
				"summary":     "Test alert from sidekick test-alert",
				"description": "This alert was sent by `sidekick test-alert` to check that alerts reach Sidekick and that its answer shows in the route's outputs. Nothing is broken.",
			},
			"startsAt":    now.UTC().Format(time.RFC3339),
			"endsAt":      "0001-01-01T00:00:00Z",
			"fingerprint": hex.EncodeToString(raw),
		}},
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode the payload: %w", err)
	}

	return body, nil
}
