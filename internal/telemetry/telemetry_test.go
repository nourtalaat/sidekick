package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace"
	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	"google.golang.org/grpc"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/config"
)

// exportTimeout bounds how long a test waits for the fake collector to
// receive spans.
const exportTimeout = 10 * time.Second

var testBuild = buildinfo.Info{Version: "v0.0.0-test", Commit: "abc123"}

func TestLogLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		level     config.LogLevel
		inSpan    bool
		logLevel  slog.Level
		wantLine  bool
		wantTrace bool
	}{
		{name: "inside a span", level: config.LogLevelInfo, inSpan: true, logLevel: slog.LevelInfo, wantLine: true, wantTrace: true},
		{name: "outside a span", level: config.LogLevelInfo, logLevel: slog.LevelInfo, wantLine: true},
		{name: "below the level", level: config.LogLevelInfo, inSpan: true, logLevel: slog.LevelDebug},
		{name: "debug level", level: config.LogLevelDebug, inSpan: true, logLevel: slog.LevelDebug, wantLine: true, wantTrace: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			tel, err := New(t.Context(), config.Telemetry{LogLevel: tt.level}, Options{Build: testBuild, LogOutput: &out})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, tel.Shutdown(context.Background())) })

			ctx := t.Context()

			var wantTraceID, wantSpanID string

			if tt.inSpan {
				var span trace.Span

				ctx, span = tel.TracerProvider().Tracer("test").Start(ctx, "test.span")
				defer span.End()

				sc := span.SpanContext()
				wantTraceID, wantSpanID = sc.TraceID().String(), sc.SpanID().String()
			}

			tel.Logger().With("component", "test").Log(ctx, tt.logLevel, "hello", "key", "value")

			if !tt.wantLine {
				require.Empty(t, out.String())

				return
			}

			var line map[string]string
			require.NoError(t, json.Unmarshal(out.Bytes(), &line))
			require.Equal(t, "hello", line["msg"])
			require.Equal(t, "test", line["component"])
			require.Equal(t, "value", line["key"])
			require.NotEmpty(t, line["time"])
			require.Equal(t, tt.logLevel.String(), line["level"])

			if !tt.wantTrace {
				require.NotContains(t, line, FieldTraceID)
				require.NotContains(t, line, FieldSpanID)

				return
			}

			require.Equal(t, wantTraceID, line[FieldTraceID])
			require.Equal(t, wantSpanID, line[FieldSpanID])
		})
	}
}

func TestRegistry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		metric string
		want   string
	}{
		{
			name:   "build info",
			metric: "sidekick_build_info",
			want: `# HELP sidekick_build_info Always 1; its labels give the version and commit of the running build.
# TYPE sidekick_build_info gauge
sidekick_build_info{commit="abc123",version="v0.0.0-test"} 1
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tel, err := New(t.Context(), config.Telemetry{}, Options{Build: testBuild, LogOutput: &bytes.Buffer{}})
			require.NoError(t, err)

			require.NoError(t, testutil.GatherAndCompare(tel.Registry(), strings.NewReader(tt.want), tt.metric))

			families, err := tel.Registry().Gather()
			require.NoError(t, err)

			names := make([]string, 0, len(families))
			for _, f := range families {
				names = append(names, f.GetName())
			}

			require.Contains(t, names, "go_goroutines")
		})
	}
}

func TestTraceExport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spanName string
	}{
		{name: "spans reach the collector with the service name", spanName: "test.span"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			collector := startCollector(t)

			tel, err := New(t.Context(), config.Telemetry{OTLPEndpoint: "http://" + collector.addr}, Options{Build: testBuild, LogOutput: &bytes.Buffer{}})
			require.NoError(t, err)

			_, span := tel.TracerProvider().Tracer("test").Start(t.Context(), tt.spanName)
			span.End()

			ctx, cancel := context.WithTimeout(t.Context(), exportTimeout)
			defer cancel()

			require.NoError(t, tel.Shutdown(ctx))

			select {
			case req := <-collector.requests:
				resource := req.GetResourceSpans()[0]

				attrs := map[string]string{}
				for _, kv := range resource.GetResource().GetAttributes() {
					attrs[kv.GetKey()] = kv.GetValue().GetStringValue()
				}

				require.Equal(t, ServiceName, attrs["service.name"])
				require.Equal(t, testBuild.Version, attrs["service.version"])
				require.Equal(t, tt.spanName, resource.GetScopeSpans()[0].GetSpans()[0].GetName())
			case <-ctx.Done():
				t.Fatal("the collector received no spans")
			}
		})
	}
}

// fakeCollector is an OTLP gRPC trace receiver that passes on every export
// request it gets.
type fakeCollector struct {
	coltracepb.UnimplementedTraceServiceServer

	addr     string
	requests chan *coltracepb.ExportTraceServiceRequest
}

func (c *fakeCollector) Export(_ context.Context, req *coltracepb.ExportTraceServiceRequest) (*coltracepb.ExportTraceServiceResponse, error) {
	c.requests <- req

	return &coltracepb.ExportTraceServiceResponse{}, nil
}

// startCollector starts a fake collector on a free local port and stops it
// when the test ends.
func startCollector(t *testing.T) *fakeCollector {
	t.Helper()

	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)

	collector := &fakeCollector{addr: listener.Addr().String(), requests: make(chan *coltracepb.ExportTraceServiceRequest, 1)}

	server := grpc.NewServer()
	coltracepb.RegisterTraceServiceServer(server, collector)

	served := make(chan error, 1)

	go func() { served <- server.Serve(listener) }()

	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, <-served, "serve the fake collector")
	})

	return collector
}
