package validate

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

const (
	// lockFile is the dependency lock file OpenTofu and Terraform write.
	lockFile = ".terraform.lock.hcl"
	// openTofuRegistry is the host OpenTofu gives providers with no host in
	// their source address.
	openTofuRegistry = "registry.opentofu.org"
	// terraformRegistry is the host in lock files that Terraform wrote. The
	// mirror serves the same providers under both hosts.
	terraformRegistry = "registry.terraform.io"
	// notInImage is the error a provider version missing from the mirror
	// gives. The docs quote it.
	notInImage = "provider version not in the validator image"
)

// providerInstallErrors start the tofu init errors that mean a provider
// could not be installed.
var providerInstallErrors = []string{
	"Failed to query available provider packages",
	"Failed to resolve provider packages",
	"Failed to install provider",
}

// mirrorFile matches a provider package in the packed mirror layout.
var mirrorFile = regexp.MustCompile(`^terraform-provider-([a-z0-9-]+)_([^_]+)_([a-z0-9]+)_([a-z0-9]+)\.zip$`)

// lockProvider and lockVersion match the lines of a lock file that name a
// provider and its locked version.
var (
	lockProvider = regexp.MustCompile(`^provider\s+"([^"]+)"\s*\{`)
	lockVersion  = regexp.MustCompile(`^\s*version\s*=\s*"([^"]+)"`)
)

// mirror maps each provider address, with the host as OpenTofu's registry,
// to the versions the mirror has for this platform.
type mirror map[string][]string

// tofuChecker checks one module directory with OpenTofu.
type tofuChecker struct {
	tofu           string
	mirror         mirror
	mirrorDir      string
	allowDownloads bool
}

// tofuValidateOutput is the part of tofu validate -json output the
// checker reads.
type tofuValidateOutput struct {
	Valid       bool `json:"valid"`
	Diagnostics []struct {
		Severity string `json:"severity"`
		Summary  string `json:"summary"`
		Detail   string `json:"detail"`
		Range    *struct {
			Filename string `json:"filename"`
			Start    struct {
				Line int `json:"line"`
			} `json:"start"`
		} `json:"range"`
	} `json:"diagnostics"`
}

// lockedProvider is one provider entry of a lock file.
type lockedProvider struct {
	address string
	version string
}

func (c tofuChecker) check(ctx context.Context, r *run, req Request) (Result, error) {
	dir := req.Dir
	if dir == "" {
		dir = "."
	}

	if res, missing := c.checkLockFile(req.Files, dir); missing {
		return res, nil
	}

	cliConfig, err := c.writeCLIConfig(r)
	if err != nil {
		return Result{}, fmt.Errorf("prepare OpenTofu: %w", err)
	}

	env := []string{
		"TF_CLI_CONFIG_FILE=" + cliConfig,
		"TF_IN_AUTOMATION=1",
		"TF_INPUT=0",
		"CHECKPOINT_DISABLE=1",
	}
	moduleDir := filepath.Join(r.module, filepath.FromSlash(dir))

	if err := os.MkdirAll(moduleDir, 0o700); err != nil {
		return Result{}, fmt.Errorf("make the module directory: %w", err)
	}

	initOut, err := r.exec(ctx, moduleDir, env, c.tofu, "init", "-backend=false", "-input=false", "-no-color")
	if err != nil {
		return Result{}, fmt.Errorf("tofu init: %w", err)
	}

	if initOut.exitCode != 0 {
		return Result{Errors: []Problem{c.initProblem(dir, initOut)}}, nil
	}

	validateOut, err := r.exec(ctx, moduleDir, env, c.tofu, "validate", "-json", "-no-color")
	if err != nil {
		return Result{}, fmt.Errorf("tofu validate: %w", err)
	}

	return tofuProblems(dir, validateOut)
}

// checkLockFile looks up each provider version the module's lock file asks
// for in the mirror. With downloads allowed OpenTofu fetches what is
// missing, so nothing is checked.
func (c tofuChecker) checkLockFile(files []File, dir string) (Result, bool) {
	if c.allowDownloads {
		return Result{}, false
	}

	name := path.Join(dir, lockFile)

	i := slices.IndexFunc(files, func(f File) bool { return f.Path == name })
	if i < 0 {
		return Result{}, false
	}

	var res Result

	for _, p := range parseLockFile(files[i].Content) {
		have := c.mirror[normalizeProvider(p.address)]
		if slices.Contains(have, p.version) {
			continue
		}

		res.Errors = append(res.Errors, Problem{Path: name, Message: missingProviderMessage(p, have)})
	}

	return res, len(res.Errors) > 0
}

// writeCLIConfig writes the OpenTofu CLI config of this run: providers come
// only from the mirror, or from the mirror and their registries when
// downloads are allowed.
func (c tofuChecker) writeCLIConfig(r *run) (string, error) {
	direct := ""
	if c.allowDownloads {
		direct = "  direct {}\n"
	}

	mirrorPath, err := json.Marshal(c.mirrorDir)
	if err != nil {
		return "", fmt.Errorf("quote the mirror path: %w", err)
	}

	config := fmt.Sprintf("provider_installation {\n  filesystem_mirror {\n    path = %s\n  }\n%s}\n", mirrorPath, direct)
	name := filepath.Join(r.home, "tofu.tfrc")

	if err := os.WriteFile(name, []byte(config), 0o600); err != nil {
		return "", fmt.Errorf("write the OpenTofu CLI config: %w", err)
	}

	return name, nil
}

// initProblem explains a failed tofu init. When providers could not be
// installed without downloads, the error says the version is not in the
// image, as a missing lock file version does.
func (c tofuChecker) initProblem(dir string, out output) Problem {
	message := strings.TrimSpace(out.stderr + "\n" + out.stdout)
	if !c.allowDownloads && slices.ContainsFunc(providerInstallErrors, func(e string) bool { return strings.Contains(message, e) }) {
		message = notInImage + " (it has " + c.mirror.summary() + "), or the version constraints match none of them; " +
			"set validator.allowDownloads to let the validator download providers. tofu init said: " + message
	}

	return Problem{Path: dir, Message: "tofu init failed: " + message}
}

// summary lists the mirror's providers and versions, for error messages.
func (m mirror) summary() string {
	parts := make([]string, 0, len(m))
	for address, versions := range m {
		parts = append(parts, address+" "+strings.Join(versions, ", "))
	}

	slices.Sort(parts)

	if len(parts) == 0 {
		return "no providers"
	}

	return strings.Join(parts, "; ")
}

// readMirror lists the provider packages in a packed mirror directory for
// this platform. Hosts are normalized, so a mirror that serves both
// registries lists each provider once.
func readMirror(dir string) (mirror, error) {
	m := mirror{}
	if dir == "" {
		return m, nil
	}

	platform := runtime.GOOS + "_" + runtime.GOARCH

	err := filepath.WalkDir(dir, func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		match := mirrorFile.FindStringSubmatch(d.Name())
		if d.IsDir() || match == nil || match[3]+"_"+match[4] != platform {
			return nil
		}

		rel, err := filepath.Rel(dir, filepath.Dir(name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}

		address := normalizeProvider(filepath.ToSlash(rel))
		if !slices.Contains(m[address], match[2]) {
			m[address] = append(m[address], match[2])
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", dir, err)
	}

	for _, versions := range m {
		slices.Sort(versions)
	}

	return m, nil
}

// parseLockFile reads the provider addresses and versions of a lock file.
// OpenTofu and Terraform write lock files in one fixed layout, so a line
// scan is enough.
func parseLockFile(content string) []lockedProvider {
	var providers []lockedProvider

	var current *lockedProvider

	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()

		if m := lockProvider.FindStringSubmatch(line); m != nil {
			providers = append(providers, lockedProvider{address: m[1]})
			current = &providers[len(providers)-1]

			continue
		}

		if m := lockVersion.FindStringSubmatch(line); m != nil && current != nil && current.version == "" {
			current.version = m[1]

			continue
		}

		if strings.HasPrefix(line, "}") {
			current = nil
		}
	}

	return providers
}

// normalizeProvider lowercases a provider address and gives it OpenTofu's
// registry as its host when it names Terraform's.
func normalizeProvider(address string) string {
	address = strings.ToLower(address)
	if rest, ok := strings.CutPrefix(address, terraformRegistry+"/"); ok {
		return openTofuRegistry + "/" + rest
	}

	return address
}

// missingProviderMessage says which locked provider version the image
// lacks and what it has instead.
func missingProviderMessage(p lockedProvider, have []string) string {
	available := "no version of it"
	if len(have) > 0 {
		available = strings.Join(have, ", ")
	}

	return fmt.Sprintf("%s %s: %s, which has %s. Lock a version the image has, or set validator.allowDownloads "+
		"to let the validator download it.", p.address, p.version, notInImage, available)
}

// tofuProblems turns tofu validate -json output into problems. Paths in
// diagnostics are relative to the module directory, so they get dir in
// front of them.
func tofuProblems(dir string, out output) (Result, error) {
	var parsed tofuValidateOutput
	if err := json.Unmarshal([]byte(out.stdout), &parsed); err != nil {
		return Result{}, fmt.Errorf("read tofu validate output (exit code %d, %s): %w",
			out.exitCode, strings.TrimSpace(out.stderr), err)
	}

	var res Result

	for _, d := range parsed.Diagnostics {
		if d.Severity != "error" {
			continue
		}

		p := Problem{Path: dir, Message: d.Summary}
		if d.Detail != "" {
			p.Message += ": " + d.Detail
		}

		if d.Range != nil && d.Range.Filename != "" {
			p.Path = path.Join(dir, filepath.ToSlash(d.Range.Filename))
			p.Line = d.Range.Start.Line
		}

		res.Errors = append(res.Errors, p)
	}

	if !parsed.Valid && len(res.Errors) == 0 {
		res.Errors = append(res.Errors, Problem{Path: dir, Message: "tofu validate found the module invalid but gave no error"})
	}

	return res, nil
}
