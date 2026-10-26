package kubernetes

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxLogRead is the most log bytes read from the API server. The result
// keeps the last maxResultSize bytes of them, since the newest lines
// matter most.
const maxLogRead = 4 * maxResultSize

// logsInput is the input of get_pod_logs.
type logsInput struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container"`
	TailLines int    `json:"tailLines"`
	Previous  bool   `json:"previous"`
	Since     string `json:"since"`
}

// getPodLogs returns the last lines of one container's logs.
func (s *Source) getPodLogs(ctx context.Context, c kubernetes.Interface, in logsInput) (source.Result, error) {
	if err := s.allowed(in.Namespace); err != nil {
		return source.Result{}, fmt.Errorf("get pod logs: %w", err)
	}

	after, err := s.since(in.Since)
	if err != nil {
		return source.Result{}, fmt.Errorf("get pod logs: %w", err)
	}

	lines := int64(limit(in.TailLines, defaultLogLines, maxLogLines))
	readLimit := int64(maxLogRead)

	opts := &corev1.PodLogOptions{
		Container:  in.Container,
		TailLines:  &lines,
		Previous:   in.Previous,
		Timestamps: true,
		LimitBytes: &readLimit,
	}

	if !after.IsZero() {
		opts.SinceTime = &metav1.Time{Time: after}
	}

	logs, err := readLogs(ctx, c, in.Namespace, in.Pod, opts)
	if err != nil {
		return source.Result{}, apiError("get", resPodLogs, in.Namespace, err)
	}

	o := object{Kind: kindPod, Resource: resourceOf(kindPod), Namespace: in.Namespace, Name: in.Pod}
	evidence := s.links.evidence(source.EvidenceLogQuery, []object{o}, logsCommand(in, lines))

	if strings.TrimSpace(logs) == "" {
		return source.Result{NoData: true, Evidence: evidence}, nil
	}

	truncated := false
	if len(logs) > maxResultSize {
		logs, truncated = lastLines(logs, maxResultSize), true
	}

	return source.Result{Text: logs, Evidence: evidence, Truncated: truncated}, nil
}

// readLogs reads the logs the options select.
func readLogs(ctx context.Context, c kubernetes.Interface, ns, pod string, opts *corev1.PodLogOptions) (string, error) {
	stream, err := c.CoreV1().Pods(ns).GetLogs(pod, opts).Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("open the log stream: %w", err)
	}

	raw, err := io.ReadAll(io.LimitReader(stream, maxLogRead))
	if closeErr := stream.Close(); err == nil && closeErr != nil {
		err = closeErr
	}

	if err != nil {
		return "", fmt.Errorf("read the log stream: %w", err)
	}

	return string(raw), nil
}

// lastLines returns the end of text, at most n bytes, starting at a line.
func lastLines(text string, n int) string {
	start := len(text) - n
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}

	tail := text[start:]
	if i := strings.IndexByte(tail, '\n'); i >= 0 && i+1 < len(tail) {
		return tail[i+1:]
	}

	return tail
}

// logsCommand is the kubectl command that shows the same logs.
func logsCommand(in logsInput, lines int64) string {
	args := []string{"logs", in.Pod, "-n", in.Namespace}
	if in.Container != "" {
		args = append(args, "-c", in.Container)
	}

	args = append(args, "--tail="+strconv.FormatInt(lines, 10), "--timestamps")
	if in.Previous {
		args = append(args, "--previous")
	}

	if _, err := time.ParseDuration(in.Since); err == nil {
		args = append(args, "--since="+in.Since)
	} else if in.Since != "" {
		args = append(args, "--since-time="+in.Since)
	}

	return kubectl(args...)
}
