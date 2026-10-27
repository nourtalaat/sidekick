package validate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidateRefusesBadRequests(t *testing.T) {
	t.Parallel()

	rules := []File{{Path: "rules.yaml", Content: "groups: []\n"}}
	many := make([]File, MaxFiles+1)

	tests := []struct {
		name string
		req  Request
		want string
	}{
		{name: "unknown format", req: Request{Format: "nagios", Files: rules}, want: `unknown format "nagios"`},
		{name: "no files", req: Request{Format: FormatPrometheusRules}, want: "no files"},
		{name: "too many files", req: Request{Format: FormatPrometheusRules, Files: many}, want: fmt.Sprintf("more than %d", MaxFiles)},
		{name: "absolute path", req: Request{Format: FormatPrometheusRules, Files: []File{{Path: "/etc/passwd"}}}, want: "clean relative path"},
		{name: "path leaves the directory", req: Request{Format: FormatPrometheusRules, Files: []File{{Path: "../rules.yaml"}}}, want: "clean relative path"},
		{name: "path not clean", req: Request{Format: FormatPrometheusRules, Files: []File{{Path: "a//rules.yaml"}}}, want: "clean relative path"},
		{name: "backslash", req: Request{Format: FormatPrometheusRules, Files: []File{{Path: `a\rules.yaml`}}}, want: "clean relative path"},
		{name: "OpenTofu data directory", req: Request{Format: FormatTerraform, Files: []File{{Path: ".terraform/providers/x"}}}, want: "clean relative path"},
		{name: "same path twice", req: Request{Format: FormatPrometheusRules, Files: append(rules, rules...)}, want: "appears twice"},
		{name: "dir outside the files", req: Request{Format: FormatTerraform, Dir: "../x", Files: rules}, want: "dir"},
		{name: "dir for a format without directories", req: Request{Format: FormatPrometheusRules, Dir: "rules", Files: rules}, want: "dir is only for format terraform"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newService(t, Options{}).Validate(t.Context(), tt.req)
			require.ErrorIs(t, err, ErrBadRequest)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestValidateRemovesTheWorkDirectory(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files []File
	}{
		{name: "one file", files: []File{fixtureFile(t, "fixture/grafana_alert_file/good/payments.yaml")}},
		{name: "files in directories", files: []File{
			{Path: "a/one.yaml", Content: "apiVersion: 1\n"},
			{Path: "a/b/two.yaml", Content: "apiVersion: 1\n"},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			work := shortTempDir(t)
			s := newService(t, Options{WorkDir: work})

			res, err := s.Validate(t.Context(), Request{Format: FormatGrafanaAlertFile, Files: tt.files})
			require.NoError(t, err)
			require.True(t, res.OK, messages(res.Errors))

			entries, err := os.ReadDir(work)
			require.NoError(t, err)
			require.Empty(t, entries)
		})
	}
}

func TestValidateWaitsForAFreeSlot(t *testing.T) {
	t.Parallel()

	req := Request{Format: FormatGrafanaAlertFile, Files: []File{{Path: "a.yaml", Content: "apiVersion: 1\n"}}}

	tests := []struct {
		name     string
		busy     int
		slotWait time.Duration
		wantErr  error
	}{
		{name: "a slot is free", busy: MaxConcurrent - 1, slotWait: time.Second},
		{name: "every slot stays busy", busy: MaxConcurrent, slotWait: 50 * time.Millisecond, wantErr: ErrBusy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newService(t, Options{})
			s.slotWait = tt.slotWait

			for range tt.busy {
				s.slots <- struct{}{}
			}

			_, err := s.Validate(t.Context(), req)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestValidateGivesUpWhenTheCallerDoes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "context cancelled while waiting for a slot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			s := newService(t, Options{})
			for range MaxConcurrent {
				s.slots <- struct{}{}
			}

			ctx, cancel := context.WithCancel(t.Context())
			cancel()

			_, err := s.Validate(ctx, Request{Format: FormatGrafanaAlertFile, Files: []File{{Path: "a.yaml"}}})
			require.ErrorIs(t, err, context.Canceled, tt.name)
		})
	}
}

func TestNewRefusesBadOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mirror  string
		workDir string
		want    string
	}{
		{name: "mirror that does not exist", mirror: filepath.Join(t.TempDir(), "missing"), want: "read the provider mirror"},
		{name: "work directory with a long path", workDir: "/" + strings.Repeat("w", MaxWorkDirLength), want: "too long for the providers' sockets"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			workDir := tt.workDir
			if workDir == "" {
				workDir = shortTempDir(t)
			}

			_, err := New(Options{Mirror: tt.mirror, WorkDir: workDir})
			require.ErrorContains(t, err, tt.want)
		})
	}
}
