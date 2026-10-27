package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// defaultChangeRange is the time range of list_changes when the call gives
// no since.
const defaultChangeRange = 24 * time.Hour

// changeLogs are the audit logs list_changes reads: Admin Activity, which
// records every call that changes a resource, and System Event, which
// records changes Google Cloud makes itself, such as GKE auto-upgrades.
var changeLogs = []string{"cloudaudit.googleapis.com%2Factivity", "cloudaudit.googleapis.com%2Fsystem_event"}

// servicePattern is the shape of a Google API service name.
var servicePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,98}\.googleapis\.com$`)

// auditLog is the part of an audit log entry's protoPayload the source
// reads.
type auditLog struct {
	ServiceName        string `json:"serviceName"`
	MethodName         string `json:"methodName"`
	ResourceName       string `json:"resourceName"`
	AuthenticationInfo struct {
		PrincipalEmail string `json:"principalEmail"`
	} `json:"authenticationInfo"`
	Status struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"status"`
}

// changeItem is one change in the result of list_changes.
type changeItem struct {
	Time      time.Time `json:"time"`
	Log       string    `json:"log"`
	Service   string    `json:"service,omitempty"`
	Method    string    `json:"method,omitempty"`
	Resource  string    `json:"resource,omitempty"`
	Principal string    `json:"principal,omitempty"`
	Severity  string    `json:"severity,omitempty"`
	// Error is the status message of a change that failed.
	Error string `json:"error,omitempty"`
}

// changesResult is the result of list_changes.
type changesResult struct {
	Project string       `json:"project"`
	Changes []changeItem `json:"changes"`
}

// listChanges runs list_changes.
func (s *Source) listChanges(ctx context.Context, input json.RawMessage) (source.Result, error) {
	in, err := decode[struct {
		Project  string `json:"project"`
		Service  string `json:"service"`
		Resource string `json:"resource"`
		Since    string `json:"since"`
		Until    string `json:"until"`
		Limit    int    `json:"limit"`
	}](input)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	project, err := s.project(in.Project)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	w, err := s.window(in.Since, in.Until, defaultChangeRange)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	filter, err := changeFilter(project, in.Service, in.Resource)
	if err != nil {
		return source.Result{}, fmt.Errorf("read the input: %w", err)
	}

	filter = withTime(filter, w)

	entries, more, err := s.listEntries(ctx, project, filter, limitOf(in.Limit, defaultEntries, maxEntries))
	if err != nil {
		return source.Result{}, fmt.Errorf("read the audit logs of %s: %w", project, err)
	}

	out := changesResult{Project: project, Changes: make([]changeItem, 0, len(entries))}
	b := budget{}

	for _, e := range entries {
		// An entry whose payload is not an audit log keeps only its time,
		// log and severity.
		var audit auditLog
		if len(e.ProtoPayload) > 0 && json.Unmarshal(e.ProtoPayload, &audit) != nil {
			audit = auditLog{}
		}

		item := changeItem{
			Time:      e.Timestamp,
			Log:       logID(e.LogName),
			Service:   audit.ServiceName,
			Method:    source.CutText(audit.MethodName, maxNameLen),
			Resource:  source.CutText(audit.ResourceName, maxNameLen),
			Principal: source.CutText(audit.AuthenticationInfo.PrincipalEmail, maxNameLen),
			Severity:  e.Severity,
		}

		if audit.Status.Code != 0 {
			item.Error = source.CutText(fmt.Sprintf("code %d: %s", audit.Status.Code, audit.Status.Message), maxMessageLen)
		}

		if !b.fits(item) {
			break
		}

		out.Changes = append(out.Changes, item)
	}

	evidence := []source.Evidence{{
		Kind:  source.EvidenceDeploy,
		Title: "Audit log changes in " + project,
		URL:   logsExplorer(project, filter, w),
		At:    w.end,
	}}

	empty := fmt.Sprintf("No changes in the audit logs of %s between %s and %s.", project, timestamp(w.start), timestamp(w.end))

	return structured(out, evidence, more || b.cut, len(out.Changes) == 0, empty)
}

// changeFilter returns the Logging filter of the audit logs of project,
// for one service and resources whose name contains resource when they
// are given. Neither may hold a quote or a backslash, which would end the
// string they go in.
func changeFilter(project, service, resource string) (string, error) {
	logs := make([]string, 0, len(changeLogs))
	for _, id := range changeLogs {
		logs = append(logs, "logName="+quote("projects/"+project+"/logs/"+id))
	}

	parts := []string{"(" + strings.Join(logs, " OR ") + ")"}

	if service = strings.TrimSpace(service); service != "" {
		if !servicePattern.MatchString(service) {
			return "", &source.ToolError{Message: fmt.Sprintf("service: %q is not a Google API name such as run.googleapis.com",
				source.CutText(service, maxNameLen))}
		}

		parts = append(parts, "protoPayload.serviceName="+quote(service))
	}

	if resource = strings.TrimSpace(resource); resource != "" {
		if len(resource) > maxNameLen || strings.ContainsAny(resource, "\"\\\n\r") {
			return "", &source.ToolError{Message: fmt.Sprintf("resource: must be at most %d bytes, without quotes, backslashes or line breaks", maxNameLen)}
		}

		parts = append(parts, "protoPayload.resourceName:"+quote(resource))
	}

	return strings.Join(parts, " AND "), nil
}
