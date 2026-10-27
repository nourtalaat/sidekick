package azure

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/source"
)

// portalURL is the root of the Azure portal.
const portalURL = "https://portal.azure.com"

// Limits on the text a result keeps, in bytes.
const (
	// maxResultSize bounds the JSON of one result. Lists stop adding items
	// once they reach it and say they were cut.
	maxResultSize = 256 << 10
	// maxMessageLen bounds one description, summary or error message.
	maxMessageLen = 2 << 10
	// maxNameLen bounds a name, title or other short text from Azure.
	maxNameLen = 512
	// maxCellLen bounds one cell of a Log Analytics result.
	maxCellLen = 1 << 10
)

// maxPages is the most pages one list call reads.
const maxPages = 5

// notFoundText is the text of a result for something that does not exist.
const notFoundText = "Not found in Azure: the subscription, resource or workspace does not exist, or Sidekick's identity cannot see it."

// budget counts the bytes a list result holds so far, so a list stops
// before it grows past maxResultSize.
type budget struct {
	used int
	cut  bool
}

// fits adds the size of item to the budget and reports whether it still
// fits. Once one item does not fit, the budget is spent.
func (b *budget) fits(item any) bool {
	if b.cut {
		return false
	}

	data, err := json.Marshal(item)
	if err != nil || b.used+len(data) > maxResultSize {
		b.cut = true

		return false
	}

	b.used += len(data)

	return true
}

// structured returns a result that holds v as JSON, with its evidence.
// When empty says the call found nothing, it returns a result with NoData
// set, the text and the evidence instead, so the link still reaches the
// report.
func structured(v any, evidence []source.Evidence, truncated, empty bool, emptyText string) (source.Result, error) {
	if len(evidence) == 0 {
		evidence = nil
	}

	if empty {
		return source.Result{NoData: true, Text: emptyText, Evidence: evidence}, nil
	}

	data, err := json.Marshal(v)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{Structured: data, Evidence: evidence, Truncated: truncated}, nil
}

// failure turns the error of a call into what Call returns. A 404 is a
// result with NoData set. Other refusals of the call itself, such as a 400
// for a query Azure cannot parse or a 403 for a permission the identity
// lacks, are tool errors with Azure's message. A 401, rate limits, server
// errors and token failures are failures of the source.
func failure(tool string, err error) (source.Result, error) {
	if se, ok := errors.AsType[*statusError](err); ok {
		switch code := se.code; {
		case code == http.StatusNotFound:
			return source.Result{NoData: true, Text: notFoundText}, nil
		case code >= http.StatusBadRequest && code < http.StatusInternalServerError &&
			code != http.StatusUnauthorized && code != http.StatusTooManyRequests:
			return source.Result{}, fmt.Errorf("call %s: %w", tool, &source.ToolError{Message: se.Error()})
		}
	}

	return source.Result{}, fmt.Errorf("call %s: %w", tool, err)
}

// portal returns the portal link of a blade of a resource, such as
// /logs or /overview.
func portal(resourceID, blade string) string {
	return portalURL + "/#resource" + resourceID + blade
}

// query returns the ARM query of an api-version and more parameters.
func query(version string, more url.Values) url.Values {
	q := url.Values{"api-version": {version}}
	maps.Copy(q, more)

	return q
}

// cut returns s cut to n bytes, and whether it was cut.
func cut(s string, n int) (string, bool) {
	c := source.CutText(s, n)

	return c, len(c) < len(s)
}
