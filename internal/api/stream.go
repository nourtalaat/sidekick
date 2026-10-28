package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

const (
	// DefaultStreamHeartbeat is how often a stream sends a comment, so
	// proxies keep the connection open, and reads the database for what a
	// missed notification left out.
	DefaultStreamHeartbeat = 15 * time.Second
	// DefaultStreamLifetime is how long a stream stays open. The browser
	// reconnects with Last-Event-ID, so the stream picks up where it
	// ended.
	DefaultStreamLifetime = 30 * time.Minute
	// streamStepBatch is how many steps a stream reads at once.
	streamStepBatch = 100
)

// The names of the events of a stream.
const (
	eventStep     = "step"
	eventStatus   = "status"
	eventWatchers = "watchers"
	// heartbeatComment is a line that EventSource ignores.
	heartbeatComment = ": heartbeat\n\n"
	// emptyEventData is the data of an event that says only that
	// something changed. An event with no data is not delivered.
	emptyEventData = "{}"
)

// errInvestigationGone means the investigation of a stream was deleted.
var errInvestigationGone = errors.New("the investigation no longer exists")

// streamStatus is the data of a status event, as the StreamStatus schema
// of the spec describes it.
type streamStatus struct {
	Status            string     `json:"status"`
	Run               int32      `json:"run"`
	RunKind           string     `json:"runKind"`
	Verdict           *string    `json:"verdict,omitempty"`
	Confidence        *string    `json:"confidence,omitempty"`
	StopReason        *string    `json:"stopReason,omitempty"`
	FinishedAt        *time.Time `json:"finishedAt,omitempty"`
	CancelRequestedAt *time.Time `json:"cancelRequestedAt,omitempty"`
	Confirmed         bool       `json:"confirmed"`
	CostMicros        int64      `json:"costMicros"`
	InputTokens       int64      `json:"inputTokens"`
	OutputTokens      int64      `json:"outputTokens"`
	ToolCalls         int32      `json:"toolCalls"`
}

// streamResponse is the answer of an open event stream. It writes the
// events itself, until the client leaves, the stream's lifetime is over or
// the hub stops.
type streamResponse struct {
	ctx       context.Context
	pool      *pgxpool.Pool
	sub       *Subscription
	log       *slog.Logger
	id        uuid.UUID
	heartbeat time.Duration
	lifetime  time.Duration

	// last is the seq of the last step sent, and lastStatus the last
	// status event's data.
	last       int32
	lastStatus []byte
}

// StreamInvestigation starts the event stream of an investigation. The
// answer holds the open stream; see streamResponse.
func (s *server) StreamInvestigation(ctx context.Context, req apigen.StreamInvestigationRequestObject) (apigen.StreamInvestigationResponseObject, error) {
	if s.hub == nil {
		return nil, problemf(http.StatusServiceUnavailable, "Live updates are not available on this server.")
	}

	after, err := streamStart(req.Params)
	if err != nil {
		return nil, err
	}

	if _, err := db.New(s.pool).GetInvestigationGroup(ctx, req.Id); errors.Is(err, pgx.ErrNoRows) {
		return nil, errNoInvestigation
	} else if err != nil {
		return nil, fmt.Errorf("read investigation %s: %w", req.Id, err)
	}

	sub, err := s.hub.Subscribe(req.Id)
	if errors.Is(err, errTooManyStreams) {
		return nil, problemf(http.StatusTooManyRequests, "This server has %d event streams open already; try again soon.", MaxStreams)
	}

	if err != nil {
		return nil, fmt.Errorf("subscribe to investigation %s: %w", req.Id, err)
	}

	// The subscription ends with the request, even when the answer is
	// never written.
	context.AfterFunc(ctx, sub.Close)

	return &streamResponse{
		ctx: ctx, pool: s.pool, sub: sub, log: s.log, id: req.Id,
		heartbeat: s.streamHeartbeat, lifetime: s.streamLifetime, last: after,
	}, nil
}

// VisitStreamInvestigationResponse writes the event stream. It never
// returns an error: once the first event is on its way, the status is
// sent, and a failure ends the stream, which the browser reconnects.
func (r *streamResponse) VisitStreamInvestigationResponse(w http.ResponseWriter) error {
	defer r.sub.Close()

	rc := http.NewResponseController(w)

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	if err := rc.Flush(); err != nil {
		r.log.WarnContext(r.ctx, "an event stream cannot be flushed", "investigation_id", r.id.String(), "error", err.Error())

		return nil
	}

	if err := r.serve(w, rc.Flush); err != nil && r.ctx.Err() == nil && !errors.Is(err, errInvestigationGone) {
		r.log.WarnContext(r.ctx, "an event stream ended with an error", "investigation_id", r.id.String(), "error", err.Error())
	}

	return nil
}

// serve sends what changed, each time the subscription wakes, and a
// heartbeat while nothing does. A heartbeat also reads the steps and the
// status, in case a notification was missed. It returns when the stream
// ends: a write failed, the client left, the lifetime is over or the hub
// stopped.
func (r *streamResponse) serve(w io.Writer, flush func() error) error {
	lifetime := time.NewTimer(r.lifetime)
	defer lifetime.Stop()

	heartbeat := time.NewTicker(r.heartbeat)
	defer heartbeat.Stop()

	changes := changeSteps | changeStatus

	for {
		if err := r.send(w, changes); err != nil {
			return err
		}

		if err := flush(); err != nil {
			return fmt.Errorf("flush: %w", err)
		}

		select {
		case <-r.sub.Wake():
			changes = r.sub.Take()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, heartbeatComment); err != nil {
				return fmt.Errorf("write the heartbeat: %w", err)
			}

			changes = changeSteps | changeStatus | r.sub.Take()
		case <-lifetime.C:
			return nil
		case <-r.ctx.Done():
			return nil
		case <-r.sub.Stopped():
			return nil
		}
	}
}

// send writes the events for changes: the new steps first, so the status
// that follows them never gets ahead of its steps, then the status, then
// the watchers.
func (r *streamResponse) send(w io.Writer, changes uint32) error {
	if changes&changeSteps != 0 {
		if err := r.sendSteps(w); err != nil {
			return fmt.Errorf("send steps: %w", err)
		}
	}

	if changes&changeStatus != 0 {
		if err := r.sendStatus(w); err != nil {
			return fmt.Errorf("send the status: %w", err)
		}
	}

	if changes&changeWatchers != 0 {
		if err := writeEvent(w, eventWatchers, nil, []byte(emptyEventData)); err != nil {
			return fmt.Errorf("send the watchers: %w", err)
		}
	}

	return nil
}

// sendSteps sends every step saved after the last one sent, a batch at a
// time.
func (r *streamResponse) sendSteps(w io.Writer) error {
	queries := db.New(r.pool)

	for {
		rows, err := queries.ListStepsPage(r.ctx, db.ListStepsPageParams{InvestigationID: r.id, After: r.last, PageSize: streamStepBatch})
		if err != nil {
			return fmt.Errorf("read the steps after %d: %w", r.last, err)
		}

		for _, row := range rows {
			step, err := stepOf(row)
			if err != nil {
				return fmt.Errorf("read step %d: %w", row.Seq, err)
			}

			data, err := json.Marshal(step)
			if err != nil {
				return fmt.Errorf("encode step %d: %w", row.Seq, err)
			}

			if err := writeEvent(w, eventStep, &row.Seq, data); err != nil {
				return fmt.Errorf("write step %d: %w", row.Seq, err)
			}

			r.last = row.Seq
		}

		if len(rows) < streamStepBatch {
			return nil
		}
	}
}

// sendStatus sends the investigation's status, unless it is what the last
// status event said.
func (r *streamResponse) sendStatus(w io.Writer) error {
	row, err := db.New(r.pool).GetInvestigationStatus(r.ctx, r.id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errInvestigationGone
	}

	if err != nil {
		return fmt.Errorf("read the status: %w", err)
	}

	data, err := json.Marshal(streamStatus{
		Status: row.Status, Run: row.Run, RunKind: row.RunKind, Verdict: row.Verdict, Confidence: row.Confidence,
		StopReason: row.StopReason, FinishedAt: row.FinishedAt, CancelRequestedAt: row.CancelRequestedAt,
		Confirmed: row.ConfirmedVerdict != nil, CostMicros: row.CostMicros, InputTokens: row.InputTokens,
		OutputTokens: row.OutputTokens, ToolCalls: row.ToolCalls,
	})
	if err != nil {
		return fmt.Errorf("encode the status: %w", err)
	}

	if string(data) == string(r.lastStatus) {
		return nil
	}

	if err := writeEvent(w, eventStatus, nil, data); err != nil {
		return fmt.Errorf("write the status: %w", err)
	}

	r.lastStatus = data

	return nil
}

// streamStart returns the seq a stream starts after: the Last-Event-ID
// header, which a reconnecting browser sends, else the after parameter,
// else before the first step.
func streamStart(p apigen.StreamInvestigationParams) (int32, error) {
	if p.LastEventID != nil && *p.LastEventID != "" {
		seq, err := strconv.ParseInt(*p.LastEventID, 10, 32)
		if err != nil || seq < beforeFirstStep {
			return 0, problemf(http.StatusBadRequest, "Last-Event-ID must be the seq of a step.")
		}

		return int32(seq), nil //nolint:gosec // ParseInt was told the value fits 32 bits.
	}

	if p.After == nil {
		return beforeFirstStep, nil
	}

	if *p.After < beforeFirstStep {
		return 0, problemf(http.StatusBadRequest, "after must be %d or more.", beforeFirstStep)
	}

	return *p.After, nil
}

// writeEvent writes one server-sent event: its id, when it has one, its
// name and its data, which holds no line break.
func writeEvent(w io.Writer, event string, id *int32, data []byte) error {
	if id != nil {
		if _, err := fmt.Fprintf(w, "id: %d\n", *id); err != nil {
			return fmt.Errorf("write the id: %w", err)
		}
	}

	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
		return fmt.Errorf("write the event: %w", err)
	}

	return nil
}
