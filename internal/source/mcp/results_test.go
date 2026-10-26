package mcp

import (
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConvertResult(t *testing.T) {
	t.Parallel()

	bigStructured := map[string]string{"log": strings.Repeat("y", maxResultSize)}

	tests := []struct {
		name     string
		res      *mcp.CallToolResult
		want     source.Result
		wantTool string
	}{
		{
			name: "text parts are joined",
			res:  &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "a"}, &mcp.TextContent{Text: "b"}}},
			want: source.Result{Text: "a\nb"},
		},
		{
			name: "images and audio are dropped",
			res: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.ImageContent{Data: []byte("png"), MIMEType: "image/png"},
				&mcp.TextContent{Text: "chart"},
				&mcp.AudioContent{Data: []byte("wav"), MIMEType: "audio/wav"},
			}},
			want: source.Result{Text: "chart"},
		},
		{
			name: "only an image is no data",
			res:  &mcp.CallToolResult{Content: []mcp.Content{&mcp.ImageContent{Data: []byte("png"), MIMEType: "image/png"}}},
			want: source.Result{NoData: true},
		},
		{
			name: "http resource links become evidence",
			res: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.ResourceLink{URI: "https://signoz.example.com/logs?q=1", Name: "logs", Title: "Error logs"},
				&mcp.ResourceLink{URI: "https://signoz.example.com/t/1", Name: "trace"},
			}},
			want: source.Result{Evidence: []source.Evidence{
				{Kind: source.EvidenceResource, Title: "Error logs", URL: "https://signoz.example.com/logs?q=1"},
				{Kind: source.EvidenceResource, Title: "trace", URL: "https://signoz.example.com/t/1"},
			}},
		},
		{
			name: "other resource links become text",
			res:  &mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{URI: "file:///var/log/app.log", Name: "app.log"}}},
			want: source.Result{Text: "Resource: app.log (file:///var/log/app.log)"},
		},
		{
			name: "embedded text resources are text",
			res: &mcp.CallToolResult{Content: []mcp.Content{
				&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "mem://a", Text: "line 1"}},
				&mcp.EmbeddedResource{Resource: &mcp.ResourceContents{URI: "mem://b", Blob: []byte{1}}},
			}},
			want: source.Result{Text: "line 1"},
		},
		{
			name: "structured content is kept",
			res:  &mcp.CallToolResult{StructuredContent: map[string]int{"count": 3}},
			want: source.Result{Structured: json.RawMessage(`{"count":3}`)},
		},
		{
			name: "oversized structured content becomes cut text",
			res:  &mcp.CallToolResult{StructuredContent: bigStructured},
			want: source.Result{Text: `{"log":"` + strings.Repeat("y", maxResultSize-len(`{"log":"`)), Truncated: true},
		},
		{
			name:     "isError keeps the text as the message",
			res:      &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "no such index"}}},
			wantTool: "no such index",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := convertResult(tt.res)
			if tt.wantTool != "" {
				var terr *source.ToolError
				require.ErrorAs(t, err, &terr)
				require.Equal(t, tt.wantTool, terr.Message)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestLimitedBody(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		size    int
		wantErr error
	}{
		{name: "at the limit", size: maxMessageSize},
		{name: "over the limit", size: maxMessageSize + 1, wantErr: errMessageTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := &limitedBody{ReadCloser: io.NopCloser(strings.NewReader(strings.Repeat("z", tt.size))), left: maxMessageSize}

			got, err := io.ReadAll(body)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Len(t, got, tt.size)
		})
	}
}
