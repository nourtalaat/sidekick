package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/slackapp"
)

const (
	// slackMethodPostMessage is the Slack Web API method that posts the
	// sample alert.
	slackMethodPostMessage = "chat.postMessage"
	// kubernetesSourceType is the type of the sources a sample pod is read from.
	kubernetesSourceType = "kubernetes"
	// madePod and madeNamespace name the pod of an alert when none could be
	// read from the cluster.
	madePod       = "checkout-7d9f8b6c5-x2k4p"
	madeNamespace = "default"
	// sampleRestarts is the restart count the sample alert states.
	sampleRestarts = 6
)

var (
	errChannelWithWebhook = errors.New("--channel posts to Slack, so it cannot be used with --webhook or --url")
	errNoSlackSection     = errors.New("the config has no slack section; test-alert --channel posts with the Slack bot token")
	errSlackRefused       = errors.New("the test alert was not accepted by Slack")
)

// podSampler is a source that can name a pod that exists.
type podSampler interface {
	SamplePod(ctx context.Context) (namespace, name string, found bool, err error)
}

// samplePodFinder names a pod of the cluster that the Kubernetes source of
// the config can read, or reports that there is none.
type samplePodFinder func(ctx context.Context, cfg *config.Config, regs config.Registries) (namespace, name string, found bool, err error)

// slackPosted is what Slack answers chat.postMessage.
type slackPosted struct {
	OK      bool            `json:"ok"`
	Error   string          `json:"error"`
	Channel string          `json:"channel"`
	TS      string          `json:"ts"`
	Message json.RawMessage `json:"message"`
}

// sendSlackTestAlert posts a sample alert into the Slack channel, as the
// bot, about a pod of the cluster when the Kubernetes source can name one,
// so that a listener on the channel takes it up like any alert. It tells
// the user which pod the alert is about and whether a listener takes it.
func sendSlackTestAlert(ctx context.Context, out io.Writer, cfg *config.Config, regs config.Registries, channel string, hc *http.Client, find samplePodFinder) error {
	if cfg.Slack == nil {
		return errNoSlackSection
	}

	token, err := secrets.ResolveText(ctx, secrets.NewDir(cfg.Secrets.Dir), cfg.Slack.BotTokenSecret)
	if err != nil {
		return fmt.Errorf("read slack.botTokenSecret, the Secret %s with key %s: %w", cfg.Slack.BotTokenSecret.Name, cfg.Slack.BotTokenSecret.Key, err)
	}

	namespace, pod, exists, err := find(ctx, cfg, regs)
	if err != nil {
		if werr := writeLine(out, "No pod could be read from the cluster: "+err.Error()); werr != nil {
			return werr
		}
	}

	if !exists {
		namespace, pod = madeNamespace, madePod
	}

	text := sampleAlertText(namespace, pod, exists)

	posted, err := postSlackAlert(ctx, hc, slackAPIURL(cfg), token, channel, text)
	if err != nil {
		return fmt.Errorf("post the test alert to %s: %w", channel, err)
	}

	return reportSlackAlert(out, cfg, channel, namespace, pod, exists, posted)
}

// reportSlackAlert tells the user what was posted and which listener takes
// it up, or why none does.
func reportSlackAlert(out io.Writer, cfg *config.Config, channel, namespace, pod string, exists bool, posted slackPosted) error {
	lines := []string{fmt.Sprintf("Posted a test alert in %s, about the pod %s/%s.", channel, namespace, pod)}
	if !exists {
		lines = append(lines, "The pod name is made up, since no pod could be read from the cluster; the alert says so.")
	}

	name, err := slackapp.TestAlertListener(cfg.Listeners, channel, posted.Channel, posted.Message)

	switch {
	case err != nil:
		lines = append(lines, "Could not check which listener takes the alert: "+err.Error())
	case name == "":
		lines = append(lines, fmt.Sprintf("No listener in the config takes this alert, so Sidekick will not investigate it: add one under listeners for %s whose match accepts bot messages (see docs/slack.md#listeners).", channel))
	default:
		lines = append(lines, fmt.Sprintf("The listener %s takes it. The reply appears in its thread within 30 seconds, and the verdict within a few minutes.", name))
	}

	for _, line := range lines {
		if err := writeLine(out, line); err != nil {
			return err
		}
	}

	return nil
}

// postSlackAlert posts the sample alert with text into channel with the bot
// token, and returns where Slack put it. The alert is one section block
// that carries the block ID the Slack app recognizes.
func postSlackAlert(ctx context.Context, hc *http.Client, apiURL, token, channel, text string) (slackPosted, error) {
	body, err := json.Marshal(map[string]any{
		"channel":      channel,
		"text":         text,
		"blocks":       slackapp.TestAlertBlocks(text),
		"link_names":   false,
		"unfurl_links": false,
		"unfurl_media": false,
	})
	if err != nil {
		return slackPosted{}, fmt.Errorf("encode the message: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, testAlertTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, apiURL+slackMethodPostMessage, bytes.NewReader(body))
	if err != nil {
		return slackPosted{}, fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := hc.Do(req)
	if err != nil {
		return slackPosted{}, fmt.Errorf("call %s: %w", slackMethodPostMessage, err)
	}

	defer func() {
		// The body is read in full or not needed; a close error changes nothing.
		_ = resp.Body.Close()
	}()

	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return slackPosted{}, fmt.Errorf("read the answer: %w", err)
	}

	var posted slackPosted
	if err := json.Unmarshal(answer, &posted); err != nil {
		return slackPosted{}, fmt.Errorf("decode the answer (HTTP status %d): %w", resp.StatusCode, err)
	}

	if !posted.OK {
		return slackPosted{}, fmt.Errorf("%w: %s%s", errSlackRefused, posted.Error, slackErrorHint(posted.Error))
	}

	return posted, nil
}

// slackErrorHint explains the Slack errors that a quick start meets.
func slackErrorHint(code string) string {
	switch code {
	case "not_in_channel", "channel_not_found":
		return " (invite the Sidekick app to the channel, and check the channel's name)"
	case "invalid_auth", "not_authed", "token_revoked", "account_inactive":
		return " (check the bot token in the Secret that slack.botTokenSecret names)"
	case "missing_scope":
		return " (the app needs the chat:write scope; create it from deploy/slack/manifest.yaml)"
	default:
		return ""
	}
}

// slackAPIURL returns the base URL of the Slack Web API, with its trailing
// slash.
func slackAPIURL(cfg *config.Config) string {
	if cfg.Slack.APIURL == "" {
		return slackapp.DefaultAPIURL
	}

	return strings.TrimSuffix(cfg.Slack.APIURL, "/") + "/"
}

// sampleAlertText is the text of the sample alert. When the pod does not
// exist, the alert says that its name is made up.
func sampleAlertText(namespace, pod string, exists bool) string {
	var b strings.Builder

	b.WriteString(":rotating_light: *Test alert: a pod keeps restarting*\n")
	fmt.Fprintf(&b, "Pod `%s/%s` restarted %d times in the last 10 minutes.\n", namespace, pod, sampleRestarts)

	if exists {
		b.WriteString("This pod runs in the cluster, and nothing may be wrong with it.\n")
	} else {
		b.WriteString("The pod name is made up: no pod could be read from the cluster.\n")
	}

	b.WriteString("Posted by `sidekick test-alert` to check that Sidekick picks up alerts in this channel.")

	return b.String()
}

// findSamplePod asks each Kubernetes source of the config, in name order,
// for a running pod, and returns the first it finds.
func findSamplePod(ctx context.Context, cfg *config.Config, regs config.Registries) (namespace, name string, found bool, err error) {
	deps := registry.Deps{Secrets: secrets.NewDir(cfg.Secrets.Dir), HTTP: httpclient.New(), Logger: slog.New(slog.DiscardHandler)}
	sources := regs.WithDefaults().Sources

	for _, sourceName := range slices.Sorted(maps.Keys(cfg.Sources)) {
		section := cfg.Sources[sourceName]
		if section.Type != kubernetesSourceType {
			continue
		}

		src, err := sources.Build(ctx, section.Type, sourceName, section, deps)
		if err != nil {
			return "", "", false, fmt.Errorf("build source %s: %w", sourceName, err)
		}

		sampler, ok := src.(podSampler)
		if !ok {
			return "", "", false, errors.Join(fmt.Errorf("source %s cannot name a pod", sourceName), closeSource(src))
		}

		namespace, name, found, err := sampler.SamplePod(ctx)
		if cerr := closeSource(src); cerr != nil {
			err = errors.Join(err, cerr)
		}

		if err != nil {
			return "", "", false, fmt.Errorf("source %s: %w", sourceName, err)
		}

		if found {
			return namespace, name, true, nil
		}
	}

	return "", "", false, nil
}

// closeSource closes a source built for one call.
func closeSource(src interface{ Close() error }) error {
	if err := src.Close(); err != nil {
		return fmt.Errorf("close the source: %w", err)
	}

	return nil
}
