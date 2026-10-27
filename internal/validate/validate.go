// Package validate checks monitor definition files before Sidekick opens a
// pull request that changes them. It runs in the sidekick-validate binary,
// in its own image with promtool, OpenTofu and a mirror of OpenTofu
// providers, so the main Sidekick image needs none of them.
package validate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Format names the format of the files in a request.
type Format string

// The formats the validator knows.
const (
	// FormatPrometheusRules is a Prometheus rules file, checked with
	// promtool check rules.
	FormatPrometheusRules Format = "prometheus_rules"
	// FormatTerraform is one OpenTofu or Terraform module directory,
	// checked with tofu init -backend=false and tofu validate.
	FormatTerraform Format = "terraform"
	// FormatGrafanaAlertFile is a Grafana alerting file provisioning file,
	// checked against its schema, with each PromQL query parsed.
	FormatGrafanaAlertFile Format = "grafana_alert_file"
)

const (
	// Timeout bounds one validation, all its files included.
	Timeout = 60 * time.Second
	// MaxConcurrent is how many validations run at the same time.
	MaxConcurrent = 2
	// SlotWait is how long a request waits for a free slot before it is
	// refused with ErrBusy.
	SlotWait = 60 * time.Second
	// MaxFiles is the most files one request may hold.
	MaxFiles = 500
	// MaxRequestBytes bounds the size of one request body.
	MaxRequestBytes = 8 << 20
	// MaxWorkDirLength bounds the length of the work directory's path.
	// OpenTofu providers listen on a Unix socket in their temporary
	// directory, <work dir>/run-<number>/home, and a socket path longer
	// than 107 bytes cannot be bound, which makes the provider exit before
	// tofu can talk to it.
	MaxWorkDirLength = 64
)

// Errors that Validate wraps, so callers can tell them apart.
var (
	// ErrBadRequest means the request itself is wrong, for example an
	// unknown format or a path that leaves the work directory.
	ErrBadRequest = errors.New("bad request")
	// ErrBusy means MaxConcurrent validations were running for all of
	// SlotWait.
	ErrBusy = errors.New("the validator is busy")
	// ErrTimeout means the validation took longer than Timeout.
	ErrTimeout = errors.New("validation timed out")
)

// File is one file to check. Path is relative and uses forward slashes.
type File struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// Request asks for one validation. A terraform request holds one module
// directory: Dir names it within Files, and is "." when Files holds only
// the module itself. Other formats check each file on its own.
type Request struct {
	Format Format `json:"format"`
	Dir    string `json:"dir,omitempty"`
	Files  []File `json:"files"`
}

// Problem is one error in a file. Line is 0 when the tool did not say.
type Problem struct {
	Path    string `json:"path"`
	Line    int    `json:"line,omitempty"`
	Message string `json:"message"`
}

// Result is the answer to a request. OK is true when no errors were
// found. NotChecked lists the parts the validator could not check, such
// as a Grafana query for a datasource that does not speak PromQL; they do
// not make OK false, but they were not checked either. Truncated is set
// when the result was cut down to fit a size limit.
type Result struct {
	OK         bool      `json:"ok"`
	Errors     []Problem `json:"errors"`
	NotChecked []Problem `json:"notChecked,omitempty"`
	Truncated  bool      `json:"truncated,omitempty"`
}

// Options configure a Service.
type Options struct {
	// Promtool and Tofu are the paths of the two binaries.
	Promtool string
	Tofu     string
	// Mirror is the OpenTofu provider mirror, in the packed layout:
	// <host>/<namespace>/<type>/terraform-provider-<type>_<version>_<os>_<arch>.zip.
	Mirror string
	// WorkDir holds one directory per running validation, removed when it
	// ends.
	WorkDir string
	// AllowDownloads lets OpenTofu download providers that are not in the
	// mirror, and remote modules. Without it, the tools get a proxy that
	// refuses every connection.
	AllowDownloads bool
	// Logger gets the validator's warnings. Nil discards them.
	Logger *slog.Logger
}

// Service runs validations, at most MaxConcurrent at a time.
type Service struct {
	opts     Options
	log      *slog.Logger
	slots    chan struct{}
	slotWait time.Duration
	checkers map[Format]checker
}

// checker checks the files of one request, written into the run's module
// directory.
type checker interface {
	check(ctx context.Context, run *run, req Request) (Result, error)
}

// New returns a Service. It reads the provider mirror once, so a mirror
// that cannot be read fails here rather than in every request, and it
// refuses a work directory longer than MaxWorkDirLength.
func New(opts Options) (*Service, error) {
	if len(opts.WorkDir) > MaxWorkDirLength {
		return nil, fmt.Errorf("the work directory %s is longer than %d bytes, too long for the providers' sockets",
			opts.WorkDir, MaxWorkDirLength)
	}

	mirror, err := readMirror(opts.Mirror)
	if err != nil {
		return nil, fmt.Errorf("read the provider mirror: %w", err)
	}

	grafana, err := newGrafanaChecker()
	if err != nil {
		return nil, fmt.Errorf("load the Grafana alert file schema: %w", err)
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Service{
		opts:     opts,
		log:      log,
		slots:    make(chan struct{}, MaxConcurrent),
		slotWait: SlotWait,
		checkers: map[Format]checker{
			FormatPrometheusRules:  promtoolChecker{promtool: opts.Promtool},
			FormatTerraform:        tofuChecker{tofu: opts.Tofu, mirror: mirror, mirrorDir: opts.Mirror, allowDownloads: opts.AllowDownloads},
			FormatGrafanaAlertFile: grafana,
		},
	}, nil
}

// Validate checks the request's files. It waits up to SlotWait for a free
// slot, then gives the validation Timeout to finish. The work directory is
// removed before it returns.
func (s *Service) Validate(ctx context.Context, req Request) (Result, error) {
	c, err := s.checkRequest(req)
	if err != nil {
		return Result{}, fmt.Errorf("check the request: %w", err)
	}

	release, err := s.acquire(ctx)
	if err != nil {
		return Result{}, fmt.Errorf("start the validation: %w", err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()

	r, err := newRun(s.opts.WorkDir, s.opts.AllowDownloads)
	if err != nil {
		return Result{}, fmt.Errorf("start the validation: %w", err)
	}

	defer func() {
		if err := r.remove(); err != nil {
			s.log.Warn("could not remove a work directory", "error", err)
		}
	}()

	if err := r.write(req.Files); err != nil {
		return Result{}, fmt.Errorf("start the validation: %w", err)
	}

	res, err := c.check(ctx, r, req)
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return Result{}, fmt.Errorf("%w after %s", ErrTimeout, Timeout)
	}

	if err != nil {
		return Result{}, fmt.Errorf("check %s files: %w", req.Format, err)
	}

	if res.Errors == nil {
		res.Errors = []Problem{}
	}

	res.OK = len(res.Errors) == 0

	return res, nil
}

// checkRequest returns the checker for the request's format, or an
// ErrBadRequest error that says what is wrong.
func (s *Service) checkRequest(req Request) (checker, error) {
	c, ok := s.checkers[req.Format]
	if !ok {
		return nil, fmt.Errorf("%w: unknown format %q; use %s, %s or %s",
			ErrBadRequest, req.Format, FormatPrometheusRules, FormatTerraform, FormatGrafanaAlertFile)
	}

	if len(req.Files) == 0 {
		return nil, fmt.Errorf("%w: no files", ErrBadRequest)
	}

	if len(req.Files) > MaxFiles {
		return nil, fmt.Errorf("%w: %d files, more than %d", ErrBadRequest, len(req.Files), MaxFiles)
	}

	if req.Dir != "" && req.Dir != "." && !isLocalPath(req.Dir) {
		return nil, fmt.Errorf("%w: dir %q must be a relative path inside the files", ErrBadRequest, req.Dir)
	}

	if req.Dir != "" && req.Format != FormatTerraform {
		return nil, fmt.Errorf("%w: dir is only for format %s", ErrBadRequest, FormatTerraform)
	}

	seen := make(map[string]bool, len(req.Files))
	for _, f := range req.Files {
		if !isLocalPath(f.Path) {
			return nil, fmt.Errorf("%w: path %q must be a clean relative path with forward slashes", ErrBadRequest, f.Path)
		}

		if seen[f.Path] {
			return nil, fmt.Errorf("%w: path %q appears twice", ErrBadRequest, f.Path)
		}

		seen[f.Path] = true
	}

	return c, nil
}

// acquire takes a slot, waiting up to SlotWait, and returns the function
// that gives it back.
func (s *Service) acquire(ctx context.Context) (func(), error) {
	wait := time.NewTimer(s.slotWait)
	defer wait.Stop()

	select {
	case s.slots <- struct{}{}:
		return func() { <-s.slots }, nil
	case <-wait.C:
		return nil, fmt.Errorf("%w: %d validations were running for %s", ErrBusy, MaxConcurrent, s.slotWait)
	case <-ctx.Done():
		return nil, fmt.Errorf("wait for a free slot: %w", ctx.Err())
	}
}

// isLocalPath reports whether p is a clean relative path with forward
// slashes that stays inside its directory and does not touch OpenTofu's
// own .terraform directory.
func isLocalPath(p string) bool {
	if p == "" || p == "." || path.Clean(p) != p || strings.ContainsAny(p, "\\\x00") {
		return false
	}

	if !filepath.IsLocal(filepath.FromSlash(p)) {
		return false
	}

	for part := range strings.SplitSeq(p, "/") {
		if part == ".terraform" {
			return false
		}
	}

	return true
}

// writeFile writes one request file under dir, with the directories it
// needs. Files are never executable.
func writeFile(dir string, f File) error {
	name := filepath.Join(dir, filepath.FromSlash(f.Path))

	if err := os.MkdirAll(filepath.Dir(name), 0o700); err != nil {
		return fmt.Errorf("make the directory of %s: %w", f.Path, err)
	}

	if err := os.WriteFile(name, []byte(f.Content), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", f.Path, err)
	}

	return nil
}
