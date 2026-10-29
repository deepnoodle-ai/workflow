package postgres

import (
	"context"
	"strings"
	"time"

	"github.com/deepnoodle-ai/workflow"
	"github.com/jackc/pgx/v5"
)

// QueueStat describes the jobs of one queue: the jobs workers claim
// together, all timers or the steps of one kind.
type QueueStat struct {
	// Kind is workflow.CommandDispatchStep for a queue of steps, or
	// workflow.CommandScheduleTimer for the timer queue.
	Kind workflow.CommandKind
	// StepKind is the kind of the queue's steps ("agent", "action", ...).
	// It is empty for the timer queue, which holds timers of every kind.
	StepKind string
	// Due counts the jobs that can be claimed now: their time has come and
	// no worker holds them under a live lease. A job whose lease expired
	// is due again.
	Due int
	// OldestDue is how long the oldest due job has been due, by the
	// database's clock. It is zero when Due is zero.
	OldestDue time.Duration
}

// queueStatsSQL lists the queues through the jobs_claim index, one index
// probe per queue, then counts each queue's due jobs.
const queueStatsSQL = `
		WITH RECURSIVE queues AS (
		    (SELECT queue FROM {{schema}}.jobs ORDER BY queue LIMIT 1)
		    UNION ALL
		    SELECT (SELECT j.queue FROM {{schema}}.jobs j
		            WHERE j.queue > q.queue ORDER BY j.queue LIMIT 1)
		    FROM queues q WHERE q.queue IS NOT NULL
		)
		SELECT q.queue, d.due,
		       coalesce((extract(epoch FROM now() - d.oldest) * 1000000)::bigint, 0)
		FROM queues q
		CROSS JOIN LATERAL (
		    SELECT count(*) AS due, min(j.run_at) AS oldest FROM {{schema}}.jobs j
		    WHERE j.queue = q.queue AND j.run_at <= now()
		      AND (j.lease_expires IS NULL OR j.lease_expires < now())
		) d
		WHERE q.queue IS NOT NULL
		ORDER BY q.queue COLLATE "C"`

// QueueStats reports, for each queue that has at least one job in the
// outbox, how many jobs are due and how long the oldest has waited: the
// queue's lag. A queue whose jobs are all in the future or claimed has Due
// zero; a queue with no jobs is not listed. The result is sorted by Kind,
// then StepKind.
//
// The cost grows with the number of due jobs, since each one's lease is
// checked: a million take a few hundred milliseconds. Poll it from one
// process, not from every worker.
func (s *Store) QueueStats(ctx context.Context) ([]QueueStat, error) {
	rows, err := s.pool.Query(ctx, s.sql(queueStatsSQL))
	if err != nil {
		return nil, err
	}
	stats, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (QueueStat, error) {
		var (
			q      QueueStat
			queue  string
			micros int64
		)
		err := row.Scan(&queue, &q.Due, &micros)
		q.OldestDue = time.Duration(micros) * time.Microsecond
		if kind, ok := strings.CutPrefix(queue, "step:"); ok {
			q.Kind, q.StepKind = workflow.CommandDispatchStep, kind
		} else {
			q.Kind = workflow.CommandScheduleTimer
		}
		return q, err
	})
	if stats == nil && err == nil {
		stats = []QueueStat{}
	}
	return stats, err
}
