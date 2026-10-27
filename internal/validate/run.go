package validate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// blockedProxy is a proxy address on a local port nothing listens on.
	// Without AllowDownloads the tools use it for every HTTP and HTTPS
	// request, so a download fails at once instead of leaving the pod.
	blockedProxy = "http://127.0.0.1:9"
	// killGrace is how long a tool may take to exit after its context ends
	// before its output pipes are closed.
	killGrace = 5 * time.Second
)

// run is the work directory of one validation: module holds the request's
// files and home is the tools' home and temporary directory.
type run struct {
	root           string
	module         string
	home           string
	allowDownloads bool
}

// output is what a tool printed and how it exited.
type output struct {
	stdout   string
	stderr   string
	exitCode int
}

// newRun makes a new work directory under workDir.
func newRun(workDir string, allowDownloads bool) (*run, error) {
	root, err := os.MkdirTemp(workDir, "run-")
	if err != nil {
		return nil, fmt.Errorf("make the work directory: %w", err)
	}

	r := &run{
		root:           root,
		module:         filepath.Join(root, "files"),
		home:           filepath.Join(root, "home"),
		allowDownloads: allowDownloads,
	}

	for _, dir := range []string{r.module, r.home} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, errors.Join(fmt.Errorf("make the work directory: %w", err), r.remove())
		}
	}

	return r, nil
}

// write writes the request's files into the module directory.
func (r *run) write(files []File) error {
	for _, f := range files {
		if err := writeFile(r.module, f); err != nil {
			return fmt.Errorf("write the request files: %w", err)
		}
	}

	return nil
}

// exec runs a tool in dir with a small, fixed environment and returns what
// it printed. A non-zero exit is not an error; failing to start the tool
// is.
func (r *run) exec(ctx context.Context, dir string, extraEnv []string, name string, args ...string) (output, error) {
	cmd := exec.CommandContext(ctx, name, args...) //nolint:gosec // name is promtool or tofu from the validator's own flags.
	cmd.Dir = dir
	cmd.Env = append(r.env(), extraEnv...)
	cmd.WaitDelay = killGrace
	setProcessGroup(cmd)

	var stdout, stderr bytes.Buffer

	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	out := output{stdout: r.scrub(stdout.String()), stderr: r.scrub(stderr.String())}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && ctx.Err() == nil {
		out.exitCode = exitErr.ExitCode()

		return out, nil
	}

	if err != nil {
		return out, fmt.Errorf("run %s: %w", filepath.Base(name), err)
	}

	return out, nil
}

// env is the environment every tool gets: nothing from the validator's own
// environment except, with downloads allowed, its proxy settings.
func (r *run) env() []string {
	env := []string{
		"HOME=" + r.home,
		"TMPDIR=" + r.home,
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}

	if !r.allowDownloads {
		for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "https_proxy", "http_proxy"} {
			env = append(env, name+"="+blockedProxy)
		}

		return append(env, "NO_PROXY=", "no_proxy=")
	}

	for _, name := range []string{"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy"} {
		if value, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+value)
		}
	}

	return env
}

// scrub removes the work directory from tool output, so paths read as the
// request named them.
func (r *run) scrub(s string) string {
	s = strings.ReplaceAll(s, r.module+string(filepath.Separator), "")

	return strings.ReplaceAll(s, r.root, "<work>")
}

// remove deletes the work directory and everything in it.
func (r *run) remove() error {
	if err := os.RemoveAll(r.root); err != nil {
		return fmt.Errorf("remove the work directory: %w", err)
	}

	return nil
}
