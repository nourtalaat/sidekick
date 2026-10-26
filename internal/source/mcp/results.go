package mcp

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxResultSize is the most text, and the largest structured content, a
// result keeps, in bytes. Longer results are cut and marked truncated.
const maxResultSize = 256 << 10

// convertResult turns an MCP tool result into a source.Result. Text and
// embedded text resources are joined, structured content is kept as JSON,
// resource links with an http or https URL become evidence, and images and
// audio are dropped. A result with isError set becomes a *source.ToolError
// with the result's text.
func convertResult(res *mcp.CallToolResult) (source.Result, error) {
	var (
		parts    []string
		evidence []source.Evidence
	)

	for _, c := range res.Content {
		switch c := c.(type) {
		case *mcp.TextContent:
			parts = append(parts, c.Text)
		case *mcp.EmbeddedResource:
			if c.Resource != nil && c.Resource.Text != "" {
				parts = append(parts, c.Resource.Text)
			}
		case *mcp.ResourceLink:
			if e, ok := linkEvidence(c); ok {
				evidence = append(evidence, e)
			} else {
				parts = append(parts, "Resource: "+linkTitle(c)+" ("+c.URI+")")
			}
		}
	}

	text, truncated := cut(strings.Join(parts, "\n"))

	if res.IsError {
		return source.Result{}, &source.ToolError{Message: text}
	}

	out := source.Result{Text: text, Evidence: evidence, Truncated: truncated}

	if res.StructuredContent != nil {
		structured, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return source.Result{}, fmt.Errorf("encode structured content: %w", err)
		}

		if len(structured) > maxResultSize {
			out.Truncated = true
			if out.Text == "" {
				out.Text, _ = cut(string(structured))
			}
		} else {
			out.Structured = structured
		}
	}

	out.NoData = out.Text == "" && len(out.Structured) == 0 && len(out.Evidence) == 0

	return out, nil
}

// linkEvidence returns the evidence for a resource link a human can open:
// one with an http or https URL.
func linkEvidence(l *mcp.ResourceLink) (source.Evidence, bool) {
	u, err := url.Parse(l.URI)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return source.Evidence{}, false
	}

	return source.Evidence{Kind: source.EvidenceResource, Title: linkTitle(l), URL: l.URI}, true
}

// linkTitle returns the best name a resource link has.
func linkTitle(l *mcp.ResourceLink) string {
	for _, s := range []string{l.Title, l.Name} {
		if s != "" {
			return cutString(s, maxDescriptionLen)
		}
	}

	return cutString(l.URI, maxDescriptionLen)
}

// cut cuts s to maxResultSize and reports whether it did.
func cut(s string) (string, bool) {
	if len(s) <= maxResultSize {
		return s, false
	}

	return cutString(s, maxResultSize), true
}

// cutString cuts s to at most n bytes without splitting a character.
func cutString(s string, n int) string {
	if len(s) <= n {
		return s
	}

	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}

	return s[:n]
}
