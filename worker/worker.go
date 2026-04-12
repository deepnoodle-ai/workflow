package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Defaults used when Config fields are left zero.
const (
	DefaultConcurrency       = 10
	DefaultPollInterval      = 5 * time.Second
	DefaultHeartbeatInterval = 30 * time.Second
	DefaultStaleAfter        = 2 * time.Minute
	DefaultReaperInterval    = 60 * time.Second
	DefaultMaxAttempts       = 3
	DefaultRunTimeout        = 30 * time.Minute
	finalizeTimeout          = 30 * time.Second
)

// Config is the worker configuration. QueueStore and Handler are the
// only required fields; everything else falls back to sane defaults.
type Config struct {
	// QueueStore is the backing persistence. Required.
	QueueStore QueueStore

	// Handler executes claimed runs. Required.
	Handler Handler

	// WorkerID identifies this worker process for lease fencing.
	// Must be stable across the worker's lifetime. Defaults to
	// "worker-<hostname>-<random>".
	WorkerID string

	// Concurrency caps the number of runs executed in parallel.
	// Defaults to DefaultConcurrency.
	Concurrency int

	// PollInterval is how often the claim loop wakes up when idle.
	// Defaults to DefaultPollInterval.
	PollInterval time.Duration

	// HeartbeatInterval is how often the worker refreshes its lease
	// on an active run. Defaults to DefaultHeartbeatInterval.
	HeartbeatInterval time.Duration

	// StaleAfter is the threshold after which a run with no recent
	// heartbeat is considered stale and eligible for reclaim or
	// dead-lettering. Must be strictly greater than HeartbeatInterval
	// to avoid spurious reclaims. Defaults to DefaultStaleAfter.
	StaleAfter time.Duration

	// ReaperInterval is how often the reaper scans for stale runs.
	// Defaults to DefaultReaperInterval.
	ReaperInterval time.Duration

	// MaxAttempts caps retries. Runs that exceed it are dead-lettered
	// to StatusFailed instead of being reclaimed. Defaults to
	// DefaultMaxAttempts.
	MaxAttempts int

	// RunTimeout is the wall-clock timeout applied to each Handler
	// invocation. Defaults to DefaultRunTimeout.
	RunTimeout time.Duration

	// Logger is the structured logger. Defaults to a discard logger.
	Logger *slog.Logger

	// Clock returns the current time. Injected for tests; defaults
	// to time.Now.
	Clock func() time.Time
}

// Worker drives a QueueStore: it claims queued runs, executes them
// via the Handler under a heartbeat lease, and reaps stale runs in
// the background.
type Worker struct {
	store   QueueStore
	handler Handler
	cfg     Config

	notify    chan struct{}
	activeIDs sync.Map
}

// New constructs a Worker from cfg. Returns an error if required
// fields are missing or if time thresholds are inconsistent.
func New(cfg Config) (*Worker, error) {
	if cfg.QueueStore == nil {
		return nil, errors.New("worker: Config.QueueStore is required")
	}
	if cfg.Handler == nil {
		return nil, errors.New("worker: Config.Handler is required")
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.HeartbeatInterval <= 0 {
		cfg.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if cfg.StaleAfter <= 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	if cfg.StaleAfter <= cfg.HeartbeatInterval {
		return nil, fmt.Errorf("worker: StaleAfter (%s) must be greater than HeartbeatInterval (%s)",
			cfg.StaleAfter, cfg.HeartbeatInterval)
	}
	if cfg.ReaperInterval <= 0 {
		cfg.ReaperInterval = DefaultReaperInterval
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = DefaultRunTimeout
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.WorkerID == "" {
		cfg.WorkerID = generateWorkerID()
	}

	return &Worker{
		store:   cfg.QueueStore,
		handler: cfg.Handler,
		cfg:     cfg,
		notify:  make(chan struct{}, 1),
	}, nil
}

// ID returns the worker's stable identifier used for lease fencing.
func (w *Worker) ID() string { return w.cfg.WorkerID }

// Notify wakes the claim loop immediately, bypassing the poll
// interval. Safe to call from any goroutine; non-blocking.
//
// Typical use: the process that enqueues a new run calls Notify
// after Enqueue to shorten pickup latency.
func (w *Worker) Notify() {
	select {
	case w.notify <- struct{}{}:
	default:
	}
}

// Run blocks until ctx is cancelled. It runs the claim loop, the
// reaper, and the heartbeat + handler goroutines for each claimed
// run. Returns ctx.Err() when the context is cancelled; any other
// return value indicates a setup error.
func (w *Worker) Run(ctx context.Context) error {
	w.cfg.Logger.Info("worker started",
		"worker_id", w.cfg.WorkerID,
		"concurrency", w.cfg.Concurrency,
	)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.reaperLoop(ctx)
	}()

	sem := make(chan struct{}, w.cfg.Concurrency)
	ticker := time.NewTicker(w.cfg.PollInterval)
	defer ticker.Stop()

	w.claimBatch(ctx, sem, &wg)
	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		case <-ticker.C:
			w.claimBatch(ctx, sem, &wg)
		case <-w.notify:
			w.claimBatch(ctx, sem, &wg)
		}
	}
}

// claimBatch drains available capacity by repeatedly calling
// ClaimQueued until the semaphore is full or no more runs are
// available.
func (w *Worker) claimBatch(ctx context.Context, sem chan struct{}, wg *sync.WaitGroup) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if len(sem) >= cap(sem) {
			return
		}
		claim, err := w.store.ClaimQueued(ctx, w.cfg.WorkerID)
		if err != nil {
			w.cfg.Logger.Error("claim failed", "error", err)
			return
		}
		if claim == nil {
			return
		}
		w.cfg.Logger.Info("claimed run", "run_id", claim.ID, "attempt", claim.Attempt)
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		wg.Add(1)
		go func(c *Claim) {
			defer wg.Done()
			defer func() { <-sem }()
			w.execute(ctx, c)
		}(claim)
	}
}

// execute runs a single claim under a heartbeat lease.
func (w *Worker) execute(parent context.Context, claim *Claim) {
	w.activeIDs.Store(claim.ID, struct{}{})
	defer w.activeIDs.Delete(claim.ID)

	runCtx, cancelRun := context.WithTimeout(parent, w.cfg.RunTimeout)
	defer cancelRun()

	hbCtx, stopHeartbeat := context.WithCancel(runCtx)
	defer stopHeartbeat()

	lease := Lease{
		RunID:    claim.ID,
		WorkerID: w.cfg.WorkerID,
		Attempt:  claim.Attempt,
	}

	var hbWG sync.WaitGroup
	hbWG.Add(1)
	go func() {
		defer hbWG.Done()
		w.heartbeat(hbCtx, cancelRun, lease)
	}()

	outcome := w.safeHandle(runCtx, claim)

	stopHeartbeat()
	hbWG.Wait()

	// Finalize with a detached context so a cancelled runCtx
	// (timeout, lease loss, parent shutdown) cannot prevent the
	// terminal write. Lease fencing still protects against double
	// writes if another worker has already reclaimed the run.
	finalizeCtx, cancelFinalize := context.WithTimeout(context.Background(), finalizeTimeout)
	defer cancelFinalize()

	if err := w.store.Complete(finalizeCtx, lease, outcome); err != nil {
		if errors.Is(err, ErrLeaseLost) {
			w.cfg.Logger.Info("lease lost at completion",
				"run_id", claim.ID, "attempt", claim.Attempt)
			return
		}
		w.cfg.Logger.Error("complete failed",
			"run_id", claim.ID, "attempt", claim.Attempt, "error", err)
		return
	}
	w.cfg.Logger.Info("run finished",
		"run_id", claim.ID, "attempt", claim.Attempt, "status", outcome.Status)
}

// safeHandle invokes the Handler and converts panics into
// StatusFailed outcomes so one bad run cannot take down the worker.
func (w *Worker) safeHandle(ctx context.Context, claim *Claim) (out Outcome) {
	defer func() {
		if r := recover(); r != nil {
			w.cfg.Logger.Error("handler panic",
				"run_id", claim.ID, "attempt", claim.Attempt, "panic", r)
			out = Outcome{
				Status:       StatusFailed,
				ErrorMessage: fmt.Sprintf("handler panic: %v", r),
			}
		}
	}()
	return w.handler.Handle(ctx, claim)
}

// heartbeat ticks the lease forward. On ErrLeaseLost it cancels the
// run's context so the Handler observes the loss and returns.
func (w *Worker) heartbeat(ctx context.Context, cancelRun context.CancelFunc, lease Lease) {
	ticker := time.NewTicker(w.cfg.HeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := w.store.Heartbeat(ctx, lease); err != nil {
				if errors.Is(err, ErrLeaseLost) {
					w.cfg.Logger.Info("heartbeat: lease lost, cancelling run",
						"run_id", lease.RunID, "attempt", lease.Attempt)
					cancelRun()
					return
				}
				// Transient errors: log and try again on the
				// next tick. The reaper will eventually reclaim
				// the run if the heartbeats never recover.
				w.cfg.Logger.Warn("heartbeat failed",
					"run_id", lease.RunID, "error", err)
			}
		}
	}
}

// reaperLoop periodically reclaims stale runs back to the queue and
// dead-letters runs that have exhausted their attempts.
func (w *Worker) reaperLoop(ctx context.Context) {
	ticker := time.NewTicker(w.cfg.ReaperInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.reapOnce(ctx)
		}
	}
}

func (w *Worker) reapOnce(ctx context.Context) {
	staleBefore := w.cfg.Clock().Add(-w.cfg.StaleAfter)

	var active []string
	w.activeIDs.Range(func(k, _ any) bool {
		active = append(active, k.(string))
		return true
	})

	deadLettered, err := w.store.DeadLetterStale(ctx, staleBefore, w.cfg.MaxAttempts, active)
	if err != nil {
		w.cfg.Logger.Error("reaper: dead-letter failed", "error", err)
	} else if len(deadLettered) > 0 {
		w.cfg.Logger.Warn("reaper dead-lettered runs",
			"count", len(deadLettered), "ids", deadLettered)
	}

	reclaimed, err := w.store.ReclaimStale(ctx, staleBefore, w.cfg.MaxAttempts, active)
	if err != nil {
		w.cfg.Logger.Error("reaper: reclaim failed", "error", err)
		return
	}
	if reclaimed > 0 {
		w.cfg.Logger.Warn("reaper reclaimed runs", "count", reclaimed)
		w.Notify()
	}
}

func generateWorkerID() string {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown"
	}
	var b [6]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("worker-%s-%s", host, hex.EncodeToString(b[:]))
}
