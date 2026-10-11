// Package background owns bounded persistence work. Queues are in memory;
// shutdown drains them, but a process crash can lose accepted work.
package background

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

var ErrSaturated = errors.New("persistence queue saturated")
var ErrClosed = errors.New("persistence writer is closed")
var ErrFailed = errors.New("persistence jobs failed; inspect persistence counters")

type Job func(context.Context) error

type Stats struct {
	Submitted uint64 `json:"submitted"`
	Completed uint64 `json:"completed"`
	Failed    uint64 `json:"failed"`
	Retried   uint64 `json:"retried"`
	Rejected  uint64 `json:"rejected"`
	Pending   uint64 `json:"pending"`
}

type task struct {
	job  Job
	ctx  context.Context
	done chan error
}

type Writer struct {
	queue                                           chan task
	options                                         Options
	ctx                                             context.Context
	cancel                                          context.CancelFunc
	done                                            chan struct{}
	mu                                              sync.Mutex
	closed                                          bool
	submitted, completed, failed, retried, rejected atomic.Uint64
}

type Options struct {
	Capacity, Workers, Attempts  int
	RetryBackoff, AttemptTimeout time.Duration
}

func New(opts Options) *Writer {
	if opts.Capacity <= 0 {
		opts.Capacity = 256
	}
	if opts.Workers <= 0 {
		opts.Workers = 2
	}
	if opts.Attempts <= 0 {
		opts.Attempts = 3
	}
	if opts.Attempts > 8 {
		opts.Attempts = 8
	}
	if opts.RetryBackoff <= 0 {
		opts.RetryBackoff = 50 * time.Millisecond
	}
	if opts.AttemptTimeout <= 0 {
		opts.AttemptTimeout = 5 * time.Second
	}
	c, cancel := context.WithCancel(context.Background())
	w := &Writer{queue: make(chan task, opts.Capacity), options: opts, ctx: c, cancel: cancel, done: make(chan struct{})}
	var workers sync.WaitGroup
	for i := 0; i < opts.Workers; i++ {
		workers.Add(1)
		go func() { defer workers.Done(); w.run() }()
	}
	go func() { workers.Wait(); close(w.done) }()
	return w
}

// Submit rejects saturated queues explicitly. The caller must handle its error.
func (w *Writer) Submit(job Job) error {
	return w.submit(task{job: job})
}

// Write waits for commit/retry completion using the same bounded worker pool.
// A canceled call can have an uncertain commit; storage must be idempotent.
func (w *Writer) Write(ctx context.Context, job Job) error {
	done := make(chan error, 1)
	if err := w.submit(task{job: job, ctx: ctx, done: done}); err != nil {
		return err
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (w *Writer) submit(t task) error {
	if w == nil || t.job == nil {
		return ErrClosed
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.rejected.Add(1)
		return ErrClosed
	}
	w.submitted.Add(1)
	select {
	case w.queue <- t:
		return nil
	default:
		w.submitted.Add(^uint64(0))
		w.rejected.Add(1)
		return ErrSaturated
	}
}

func (w *Writer) run() {
	for t := range w.queue {
		err := w.execute(t)
		if err == nil {
			w.completed.Add(1)
		} else {
			w.failed.Add(1)
			slog.Error("persistence job failed", "attempts", w.options.Attempts)
		}
		if t.done != nil {
			t.done <- err
		}
	}
}
func (w *Writer) execute(t task) error {
	ctx := t.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.ctx, cancel)
	defer stop()
	defer cancel()
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		writeCtx, writeCancel := context.WithTimeout(ctx, w.options.AttemptTimeout)
		err := t.job(writeCtx)
		writeCancel()
		if err == nil || attempt+1 >= w.options.Attempts {
			return err
		}
		w.retried.Add(1)
		timer := time.NewTimer(w.options.RetryBackoff << attempt)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}
}

// Close stops admission and drains accepted jobs. Deadline cancellation stops
// retries; callers must report the error before closing the database.
func (w *Writer) Close(ctx context.Context) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	if !w.closed {
		w.closed = true
		close(w.queue)
	}
	w.mu.Unlock()
	select {
	case <-w.done:
		w.cancel()
		if w.failed.Load() > 0 {
			return ErrFailed
		}
		return nil
	case <-ctx.Done():
		w.cancel()
		return ctx.Err()
	}
}
func (w *Writer) Stats() Stats {
	if w == nil {
		return Stats{}
	}
	stats := Stats{Submitted: w.submitted.Load(), Completed: w.completed.Load(), Failed: w.failed.Load(), Retried: w.retried.Load(), Rejected: w.rejected.Load()}
	if stats.Submitted > stats.Completed+stats.Failed {
		stats.Pending = stats.Submitted - stats.Completed - stats.Failed
	}
	return stats
}
