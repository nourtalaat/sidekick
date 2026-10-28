package memory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// backfillBatch is how many embed_memory jobs the start-up backfill
// enqueues in one transaction.
const backfillBatch = 500

// embedUniqueKeyPrefix starts the unique key of an embed_memory job, which
// is followed by the investigation ID.
const embedUniqueKeyPrefix = "embed_memory:"

// errEntryChanged is returned by a job that embedded an entry which changed
// meanwhile. The job runs again and reads the new text.
var errEntryChanged = errors.New("the memory entry changed while it was embedded")

// embedPayload is the payload of an embed_memory job.
type embedPayload struct {
	InvestigationID uuid.UUID `json:"investigationId"`
}

// EmbedJob embeds memory entries and stores their vectors. Register it with
// the queue as the handler of queue.KindEmbedMemory.
type EmbedJob struct {
	pool *pgxpool.Pool
	vec  *Vectors
	log  *slog.Logger
}

// NewEmbedJob returns the handler of embed_memory jobs. A nil logger logs
// nothing.
func NewEmbedJob(pool *pgxpool.Pool, vec *Vectors, logger *slog.Logger) *EmbedJob {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}

	return &EmbedJob{pool: pool, vec: vec, log: logger}
}

// Handle embeds the entry the job names and stores its vector, replacing an
// older one. It calls the embeddings model outside any transaction. The
// vector is stored only while the entry still has the text that was
// embedded, so a change in between makes the job run again instead of
// leaving an old vector; the job gives up its unique key in the transaction
// that stores the vector, so a change after that enqueues a new job. An
// entry that is gone has nothing to embed. Errors that repeating cannot fix,
// such as a model that returns vectors of the wrong length, end the job.
func (j *EmbedJob) Handle(ctx context.Context, job db.Job) error {
	var p embedPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return fmt.Errorf("decode the payload of job %d: %w: %w", job.ID, err, queue.ErrGiveUp)
	}

	row, err := db.New(j.pool).GetMemoryEmbedText(ctx, p.InvestigationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return j.store(ctx, job, p.InvestigationID, nil, "")
	}

	if err != nil {
		return fmt.Errorf("read the text of memory entry %s: %w", p.InvestigationID, err)
	}

	res, err := j.vec.embedText(ctx, row.Text)
	if err != nil {
		return fmt.Errorf("memory entry %s: %w", p.InvestigationID, giveUpIfInvalid(err))
	}

	return j.store(ctx, job, p.InvestigationID, res.Vectors[0], row.TextHash)
}

// store writes the vector of an entry, when there is one, and gives up the
// job's unique key, in one transaction. A nil vector means the entry is gone.
func (j *EmbedJob) store(ctx context.Context, job db.Job, id uuid.UUID, vector []float32, textHash string) (err error) {
	tx, err := j.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	if vector != nil {
		if len(vector) != j.vec.Dimensions() {
			return fmt.Errorf("memory entry %s: the vector has %d numbers, not %d: %w", id, len(vector), j.vec.Dimensions(), queue.ErrGiveUp)
		}

		rows, err := db.New(tx).UpsertMemoryEmbedding(ctx, db.UpsertMemoryEmbeddingParams{
			Model:           j.vec.Model(),
			Dimensions:      int32(len(vector)), //nolint:gosec // Checked against the configured dimensions, at most 2,000.
			Embedding:       FormatVector(vector),
			InvestigationID: id,
			TextHash:        textHash,
		})
		if err != nil {
			return fmt.Errorf("store the embedding of memory entry %s: %w", id, err)
		}

		if rows == 0 {
			return fmt.Errorf("memory entry %s: %w", id, errEntryChanged)
		}
	}

	if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
		return fmt.Errorf("give up the unique key: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// Backfill enqueues an embed_memory job for every memory entry that has no
// embedding, or one from another model than vec's, in batches, and returns
// how many entries it found. Call it once at start-up with vectors on. An
// entry that has a job already is left to it.
func Backfill(ctx context.Context, pool *pgxpool.Pool, vec *Vectors) (int, error) {
	var (
		after = uuid.Nil
		total int
	)

	for {
		ids, err := db.New(pool).ListMemoryNeedingEmbedding(ctx, db.ListMemoryNeedingEmbeddingParams{
			After: after, Model: vec.Model(), MaxRows: backfillBatch,
		})
		if err != nil {
			return total, fmt.Errorf("list the entries that need an embedding: %w", err)
		}

		if len(ids) == 0 {
			return total, nil
		}

		if err := enqueueAll(ctx, pool, ids); err != nil {
			return total, err
		}

		total += len(ids)
		after = ids[len(ids)-1]
	}
}

// enqueueAll enqueues an embed_memory job for each of ids, in one
// transaction.
func enqueueAll(ctx context.Context, pool *pgxpool.Pool, ids []uuid.UUID) (err error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(context.WithoutCancel(ctx)); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	for _, id := range ids {
		if err := enqueueEmbed(ctx, tx, id); err != nil {
			return fmt.Errorf("enqueue the embedding of memory entry %s: %w", id, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// enqueueEmbed enqueues the embed_memory job of an entry through q. An entry
// that has a pending or running job already is left to it.
func enqueueEmbed(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	payload, err := json.Marshal(embedPayload{InvestigationID: id})
	if err != nil {
		return fmt.Errorf("encode the payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{Kind: queue.KindEmbedMemory, Payload: payload, UniqueKey: embedUniqueKeyPrefix + id.String()})
	if errors.Is(err, queue.ErrDuplicate) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("enqueue the %s job: %w", queue.KindEmbedMemory, err)
	}

	return nil
}

// giveUpIfInvalid wraps queue.ErrGiveUp into err when it is an embeddings
// error that repeating the job cannot fix: an invalid request, or a text the
// model cannot take. Other errors are left to the job's retries.
func giveUpIfInvalid(err error) error {
	var merr *model.Error
	if errors.As(err, &merr) && (merr.Kind == model.ErrInvalid || merr.Kind == model.ErrContextLength) {
		return fmt.Errorf("%w: %w", err, queue.ErrGiveUp)
	}

	return err
}
