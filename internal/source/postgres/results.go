package postgres

import (
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxQueryLen bounds the text of one query in a result, in bytes. Postgres
// keeps only the first track_activity_query_size bytes (1 KiB by default)
// of a running query anyway.
const maxQueryLen = 2 << 10

// structured returns a result that holds v as JSON, with evidence that
// names the views it read, so a person can run the same query.
func structured(req request, views string, v any, truncated bool) (source.Result, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return source.Result{}, fmt.Errorf("encode the result: %w", err)
	}

	return source.Result{
		Structured: data,
		Evidence:   []source.Evidence{{Kind: source.EvidenceOther, Title: views + " in database " + req.database}},
		Truncated:  truncated,
	}, nil
}

// noData returns a result that found nothing, with text that says what was
// looked for.
func noData(format string, args ...any) source.Result {
	return source.Result{NoData: true, Text: fmt.Sprintf(format, args...)}
}

// collect reads the rows of a query that asked for one more row than
// limit, into structs by column name. It returns at most limit rows, and
// whether there were more.
func collect[T any](rows pgx.Rows, limit int) ([]T, bool, error) {
	items, err := pgx.CollectRows(rows, pgx.RowToStructByName[T])
	if err != nil {
		return nil, false, fmt.Errorf("read the rows: %w", err)
	}

	if len(items) > limit {
		return items[:limit], true, nil
	}

	return items, false, nil
}

// cutQuery cuts the text of a query to maxQueryLen bytes. It returns nil
// for nil, which is what the queries return when query text is left out.
func cutQuery(query *string) *string {
	if query == nil {
		return nil
	}

	cut := source.CutText(*query, maxQueryLen)

	return &cut
}
