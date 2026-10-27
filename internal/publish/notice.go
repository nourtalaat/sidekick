package publish

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// NoticeSpanName is the name of the span of one notice job.
const NoticeSpanName = "output.notice"

// attrNoticeKind is the span attribute of the notice's kind.
const attrNoticeKind = "sidekick.notice.kind"

// NoticeConcurrency is how many notice jobs one replica runs at once.
const NoticeConcurrency = 2

var errNoNoticeSink = errors.New("the notice payload has no sink")

// Notice is a notice to send to some of the configured sinks, at most once
// per window of its key.
type Notice struct {
	// Key names the notice for its window, such as
	// "daily_cap:route:payments".
	Key string
	// At is when the notice is sent.
	At time.Time
	// NotBefore starts the window: a notice with the same key sent at or
	// after it means this one is not sent.
	NotBefore time.Time
	// Sinks are the outputs to send it to, usually the route's. Those that
	// cannot take a notice are skipped when the job runs.
	Sinks []string
	// Notice is what the sinks show.
	Notice output.Notice
}

// noticePayload is the payload of a notice job.
type noticePayload struct {
	Sink   string        `json:"sink"`
	Notice output.Notice `json:"notice"`
}

// SendNotice claims n's key for its window and, when no notice with the
// key was sent in the window yet, enqueues a notice job for each of its
// sinks. It reports whether it enqueued them. The claim is one upsert, so
// of two calls at once, from any replicas, only one sends. Call it in the
// transaction that records what the notice is about.
func SendNotice(ctx context.Context, q db.DBTX, n Notice) (bool, error) {
	_, err := db.New(q).ClaimNotice(ctx, db.ClaimNoticeParams{Key: n.Key, SentAt: n.At, NotBefore: n.NotBefore})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("claim notice %s: %w", n.Key, err)
	}

	for _, sink := range n.Sinks {
		payload, err := json.Marshal(noticePayload{Sink: sink, Notice: n.Notice})
		if err != nil {
			return false, fmt.Errorf("encode the notice payload: %w", err)
		}

		if _, err := queue.Enqueue(ctx, q, queue.NewJob{Kind: queue.KindNotice, Payload: payload, MaxAttempts: jobMaxAttempts}); err != nil {
			return false, fmt.Errorf("enqueue notice %s for sink %q: %w", n.Key, sink, err)
		}
	}

	return true, nil
}

// NoticeHandler runs notice jobs. Make it with NewNoticeHandler and
// register it for queue.KindNotice.
type NoticeHandler struct {
	sinks  map[string]output.Sink
	log    *slog.Logger
	tracer trace.Tracer
}

var _ queue.Handler = (*NoticeHandler)(nil)

// NewNoticeHandler returns a NoticeHandler for the sinks in opts. It uses
// Sinks, Logger and TracerProvider; the notice jobs need no database.
func NewNoticeHandler(opts Options) *NoticeHandler {
	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &NoticeHandler{sinks: opts.Sinks, log: log, tracer: tp.Tracer(tracerName)}
}

// Handle sends the notice of one job to its sink, inside an output.notice
// span that is a child of the span that enqueued the job. A sink that
// cannot take notices is skipped. A failed send returns the error, so the
// job is retried; a payload that does not decode, or a sink that is no
// longer configured, gives up the job.
func (h *NoticeHandler) Handle(ctx context.Context, job db.Job) (err error) {
	ctx, span := h.tracer.Start(queue.TraceContext(ctx, job), NoticeSpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "notice failed")
		}

		span.End()
	}()

	var p noticePayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode the notice payload: %w: %w", queue.ErrGiveUp, err)
	}

	if p.Sink == "" {
		return fmt.Errorf("decode the notice payload: %w: %w", queue.ErrGiveUp, errNoNoticeSink)
	}

	span.SetAttributes(attribute.String(attrSink, p.Sink), attribute.String(attrNoticeKind, string(p.Notice.Kind)))

	sink, ok := h.sinks[p.Sink]
	if !ok {
		return fmt.Errorf("sink %q: %w: %w", p.Sink, queue.ErrGiveUp, errUnknownSink)
	}

	noticer, ok := sink.(output.Noticer)
	if !ok {
		h.log.DebugContext(ctx, "notice skipped: the sink cannot take notices", "sink", p.Sink, "kind", p.Notice.Kind)

		return nil
	}

	if err := noticer.Notice(ctx, p.Notice); err != nil {
		return fmt.Errorf("send the %s notice to %s: %w", p.Notice.Kind, p.Sink, err)
	}

	h.log.InfoContext(ctx, "notice sent", "sink", p.Sink, "kind", p.Notice.Kind, "job_id", job.ID)

	return nil
}
