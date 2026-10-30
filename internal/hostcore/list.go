package hostcore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/deepnoodle-ai/workflow/durable"
)

// The page sizes of ListRuns.
const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// DeleteBatch is how many runs one transaction of DeleteEndedRuns deletes
// at most. The stores' docs quote it.
const DeleteBatch = 500

// RunQuery is a checked RunFilter, as a backend reads it.
type RunQuery struct {
	Status        []string
	Metadata      map[string]string
	CreatedAfter  time.Time // zero: no bound
	CreatedBefore time.Time // zero: no bound
	UpdatedAfter  time.Time // zero: no bound
	// After is the position to read after, from the cursor; nil reads from
	// the newest run.
	After *Position
	// Limit is the most rows to return.
	Limit int
}

// Position is a run's place in the list order: created_at, then ID, both
// descending.
type Position struct {
	CreatedAt time.Time
	ID        string
}

// cursor is a Position as RunPage.Next encodes it.
type cursor struct {
	T  time.Time `json:"t"`
	ID string    `json:"id"`
}

// ListRuns reads one page of the runs f selects (PRD 011). It checks f,
// asks the backend for one row more than the page holds, and makes the
// next page's cursor from the last row it keeps.
func (s *Store) ListRuns(ctx context.Context, b Backend, f durable.RunFilter) (durable.RunPage, error) {
	q, err := s.runQuery(f)
	if err != nil {
		return durable.RunPage{}, err
	}

	rows, err := b.ListRuns(ctx, q)
	if err != nil {
		return durable.RunPage{}, fmt.Errorf("%s: list runs: %w", s.Prefix, err)
	}

	page := durable.RunPage{Runs: rows}
	if len(rows) >= q.Limit {
		page.Runs = rows[:q.Limit-1]
		last := page.Runs[len(page.Runs)-1]
		data, err := json.Marshal(cursor{T: last.CreatedAt.UTC(), ID: last.ID})
		if err != nil {
			return durable.RunPage{}, err
		}
		page.Next = base64.RawURLEncoding.EncodeToString(data)
	}
	return page, nil
}

// runQuery checks f and turns it into the query a backend runs. Its Limit
// is one more than the page holds.
func (s *Store) runQuery(f durable.RunFilter) (RunQuery, error) {
	q := RunQuery{
		CreatedAfter:  f.CreatedAfter,
		CreatedBefore: f.CreatedBefore,
		UpdatedAfter:  f.UpdatedAfter,
	}

	switch {
	case f.Limit < 0:
		return q, fmt.Errorf("%s: list runs: the limit %d is negative", s.Prefix, f.Limit)
	case f.Limit == 0:
		q.Limit = defaultListLimit + 1
	default:
		q.Limit = min(f.Limit, maxListLimit) + 1
	}

	for _, st := range f.Status {
		q.Status = append(q.Status, string(st))
	}

	keys := make([]string, 0, len(f.Metadata))
	for k := range f.Metadata {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		v, ok := f.Metadata[k].(string)
		if !ok {
			return q, fmt.Errorf("%s: list runs: metadata %q: the value must be a string, not %T",
				s.Prefix, k, f.Metadata[k])
		}
		if q.Metadata == nil {
			q.Metadata = make(map[string]string, len(keys))
		}
		q.Metadata[k] = v
	}

	if f.Cursor != "" {
		pos, err := decodeCursor(f.Cursor)
		if err != nil {
			return q, fmt.Errorf("%s: list runs: invalid cursor", s.Prefix)
		}
		q.After = pos
	}
	return q, nil
}

func decodeCursor(s string) (*Position, error) {
	data, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	var c cursor
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, err
	}
	if c.T.IsZero() || c.ID == "" {
		return nil, errors.New("incomplete cursor")
	}
	return &Position{CreatedAt: c.T, ID: c.ID}, nil
}

// DeleteEndedRuns deletes the ended runs whose updated_at and finished_at
// are before before, with their steps, events and jobs, DeleteBatch runs
// per transaction (PRD 011). It stops when a batch deletes no run, or
// between batches when ctx ends, and returns how many runs it deleted. A
// short batch does not stop it: a backend may skip a run another
// transaction holds, and eligible runs can remain after it.
func (s *Store) DeleteEndedRuns(ctx context.Context, b Backend, before time.Time) (int, error) {
	total := 0
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := b.DeleteEndedRuns(ctx, before, DeleteBatch)
		total += n
		if err != nil {
			return total, fmt.Errorf("%s: delete ended runs: %w", s.Prefix, err)
		}
		if n == 0 {
			return total, nil
		}
	}
}
