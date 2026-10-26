package mcp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Type is the source type this package registers.
const Type = "mcp"

// clientName is the name Sidekick gives itself to MCP servers.
const clientName = "sidekick"

// Timings of the connection.
const (
	// connectTimeout bounds one attempt to connect and list the tools.
	connectTimeout = 10 * time.Second
	// refreshInterval is how often the tool list is fetched again.
	refreshInterval = 10 * time.Minute
	// listTimeout bounds one refresh of the tool list.
	listTimeout = 10 * time.Second
	// firstBackoff and maxBackoff bound the wait between connect attempts,
	// which doubles after each failure.
	firstBackoff = time.Second
	maxBackoff   = time.Minute
)

// Limits on what a server can make Sidekick hold.
const (
	// maxTools is the most tools one server may list.
	maxTools = 500
	// maxDescriptionLen is the longest tool description kept, in bytes.
	maxDescriptionLen = 2048
)

var (
	// errNoSecrets is returned when a value names a secret but the source
	// is built without a way to read secrets.
	errNoSecrets = errors.New("no secrets resolver in deps")
	// errTooManyTools is returned when a server lists more than maxTools.
	errTooManyTools = fmt.Errorf("the server lists more than %d tools", maxTools)
)

func init() {
	registry.Sources.Register(Type, registry.Typed(New))
}

// Source is one MCP server. It is safe for concurrent use.
type Source struct {
	name   string
	cfg    Config
	logger *slog.Logger
	client *mcp.Client
	// transport makes a new transport for each connect attempt.
	transport func(ctx context.Context) (mcp.Transport, error)
	// refresh is signalled when the server says its tool list changed.
	refresh chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	close   sync.Once

	mu      sync.RWMutex
	session *mcp.ClientSession
	tools   []source.Tool
}

var _ source.Source = (*Source)(nil)

// Tools returns the tools the server listed last, before allowlist
// filtering. Tools the annotations mark as writing have ReadOnly false,
// unless tools.trustAnnotations is false. It returns an error that wraps
// source.ErrUnavailable while the server is not connected.
func (s *Source) Tools(_ context.Context) ([]source.Tool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.session == nil {
		return nil, fmt.Errorf("list tools of %s: %w", s.name, source.ErrUnavailable)
	}

	return append([]source.Tool(nil), s.tools...), nil
}

// Call runs one tool on the server, within the source's timeout and the
// call's deadline. A tool that reports an error returns a
// *source.ToolError; a server that is not connected returns an error that
// wraps source.ErrUnavailable.
func (s *Source) Call(ctx context.Context, call source.Call) (source.Result, error) {
	session := s.current()
	if session == nil {
		return source.Result{}, fmt.Errorf("call %s on %s: %w", call.Tool, s.name, source.ErrUnavailable)
	}

	ctx, cancel := context.WithTimeout(ctx, s.cfg.CallTimeout())
	defer cancel()

	if !call.Deadline.IsZero() {
		var cancelDeadline context.CancelFunc

		ctx, cancelDeadline = context.WithDeadline(ctx, call.Deadline)
		defer cancelDeadline()
	}

	params := &mcp.CallToolParams{Name: call.Tool}
	if len(call.Input) > 0 {
		params.Arguments = call.Input
	}

	res, err := session.CallTool(ctx, params)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return source.Result{}, fmt.Errorf("call %s on %s: %w", call.Tool, s.name, ctxErr)
		}

		return source.Result{}, fmt.Errorf("call %s on %s: %w", call.Tool, s.name, err)
	}

	return convertResult(res)
}

// Close stops the connection loop, closes the session and, for stdio,
// stops the child process. It is safe to call more than once.
func (s *Source) Close() error {
	s.close.Do(func() {
		s.cancel()
		<-s.done
	})

	return nil
}

// Up reports whether the server is connected. The guard reads it for the
// sidekick_source_up metric.
func (s *Source) Up() bool {
	return s.current() != nil
}

// current returns the connected session, or nil.
func (s *Source) current() *mcp.ClientSession {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.session
}

// run connects, keeps the tool list fresh while connected, and connects
// again with backoff whenever the session ends, until ctx ends.
func (s *Source) run(ctx context.Context) {
	defer close(s.done)

	backoff := firstBackoff

	for ctx.Err() == nil {
		session, end, err := s.connect(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}

			s.logger.Warn("cannot connect to the MCP server; will retry", "error", err, "retry_in", backoff)

			if !sleep(ctx, backoff) {
				return
			}

			backoff = min(2*backoff, maxBackoff)

			continue
		}

		backoff = firstBackoff

		s.logger.Info("connected to the MCP server")
		s.serve(ctx, session)
		end()
	}
}

// connect opens a session and lists the tools, within connectTimeout. The
// session becomes current only once the tools are listed. The session
// lives in a context of its own, because some transports keep a stream
// open with the context they connect with; the returned function ends it.
func (s *Source) connect(ctx context.Context) (*mcp.ClientSession, context.CancelFunc, error) {
	ctx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(connectTimeout, cancel)

	session, tools, err := s.open(ctx)
	if !timer.Stop() && err == nil {
		s.closeSession(session)
		err = context.DeadlineExceeded
	}

	if err != nil {
		cancel()

		return nil, nil, fmt.Errorf("open session: %w", err)
	}

	s.mu.Lock()
	s.session = session
	s.tools = tools
	s.mu.Unlock()

	return session, cancel, nil
}

// open makes a transport, connects and lists the tools.
func (s *Source) open(ctx context.Context) (*mcp.ClientSession, []source.Tool, error) {
	t, err := s.transport(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("make transport: %w", err)
	}

	session, err := s.client.Connect(ctx, t, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("connect: %w", err)
	}

	tools, err := s.list(ctx, session)
	if err != nil {
		s.closeSession(session)

		return nil, nil, fmt.Errorf("list tools: %w", err)
	}

	return session, tools, nil
}

// serve refreshes the tool list of a connected session on the server's
// notification and every refreshInterval, and returns when the session
// ends or ctx ends. Either way the source is no longer connected.
func (s *Source) serve(ctx context.Context, session *mcp.ClientSession) {
	ended := make(chan error, 1)

	go func() { ended <- session.Wait() }()

	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()

	defer func() {
		s.mu.Lock()
		s.session = nil
		s.mu.Unlock()
	}()

	for {
		select {
		case <-ctx.Done():
			s.closeSession(session)
			<-ended

			return
		case err := <-ended:
			s.logger.Warn("lost the MCP session; reconnecting", "error", err)

			return
		case <-s.refresh:
			s.refreshTools(ctx, session)
		case <-ticker.C:
			s.refreshTools(ctx, session)
		}
	}
}

// refreshTools lists the tools again. On failure it keeps the old list.
func (s *Source) refreshTools(ctx context.Context, session *mcp.ClientSession) {
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	tools, err := s.list(ctx, session)
	if err != nil {
		s.logger.Warn("cannot refresh the MCP tool list; keeping the old one", "error", err)

		return
	}

	s.mu.Lock()
	s.tools = tools
	s.mu.Unlock()
}

// list fetches every page of the server's tool list.
func (s *Source) list(ctx context.Context, session *mcp.ClientSession) ([]source.Tool, error) {
	var tools []source.Tool

	for t, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list tools: %w", err)
		}

		if len(tools) == maxTools {
			return nil, errTooManyTools
		}

		tool, err := convertTool(t, s.cfg.Tools.TrustsAnnotations())
		if err != nil {
			s.logger.Warn("skipping an MCP tool Sidekick cannot read", "tool", t.Name, "error", err)

			continue
		}

		tools = append(tools, tool)
	}

	return tools, nil
}

// closeSession closes a session, logging a failure, which only matters
// for debugging.
func (s *Source) closeSession(session *mcp.ClientSession) {
	if err := session.Close(); err != nil {
		s.logger.Debug("close the MCP session", "error", err)
	}
}

// onToolsChanged asks the connection loop to list the tools again. A
// refresh that is already pending covers this one too.
func (s *Source) onToolsChanged(context.Context, *mcp.ToolListChangedRequest) {
	select {
	case s.refresh <- struct{}{}:
	default:
	}
}

// New builds an MCP source from its validated config. It returns at once:
// the connection is made in the background and retried until it works, so
// a server that is down blocks neither start-up nor readiness. Until it is
// connected, Tools and Call return errors that wrap source.ErrUnavailable.
func New(ctx context.Context, name string, cfg Config, deps registry.Deps) (source.Source, error) {
	if err := cfg.Check(name); err != nil {
		return nil, fmt.Errorf("check settings: %w", err)
	}

	if cfg.usesSecrets() && deps.Secrets == nil {
		return nil, errNoSecrets
	}

	logger := cmp.Or(deps.Logger, slog.Default()).With("component", "source", "source", name)

	s := &Source{
		name:    name,
		cfg:     cfg,
		logger:  logger,
		refresh: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}

	s.transport = newTransportFunc(cfg, deps)
	s.client = mcp.NewClient(&mcp.Implementation{Name: clientName, Version: buildinfo.Version}, &mcp.ClientOptions{
		Logger:                 logger,
		Capabilities:           &mcp.ClientCapabilities{},
		ToolListChangedHandler: s.onToolsChanged,
	})

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	s.cancel = cancel

	go s.run(runCtx)

	return s, nil
}

// convertTool turns an MCP tool into a source.Tool. With trust set, a tool
// is read-only only when its annotations say so, or when it has none.
func convertTool(t *mcp.Tool, trust bool) (source.Tool, error) {
	schema, err := json.Marshal(t.InputSchema)
	if err != nil {
		return source.Tool{}, fmt.Errorf("encode the input schema: %w", err)
	}

	return source.Tool{
		Name:        t.Name,
		Description: source.CutText(t.Description, maxDescriptionLen),
		InputSchema: schema,
		ReadOnly:    !trust || readOnly(t.Annotations),
	}, nil
}

// readOnly applies the annotation rule: no annotations means no claim, so
// the tool is kept; with annotations, readOnlyHint must be true and
// destructiveHint must not be true.
func readOnly(a *mcp.ToolAnnotations) bool {
	if a == nil {
		return true
	}

	return a.ReadOnlyHint && (a.DestructiveHint == nil || !*a.DestructiveHint)
}

// sleep waits for d or until ctx ends, and reports whether d passed.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
