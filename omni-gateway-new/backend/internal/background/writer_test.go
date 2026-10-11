package background

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteRetriesAndDrains(t *testing.T) {
	w := New(Options{Workers: 1, Attempts: 3, RetryBackoff: time.Millisecond})
	var calls atomic.Int32
	err := w.Write(context.Background(), func(context.Context) error {
		if calls.Add(1) < 3 {
			return errors.New("temporary")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	stats := w.Stats()
	if calls.Load() != 3 || stats.Completed != 1 || stats.Retried != 2 || stats.Pending != 0 {
		t.Fatalf("calls=%d stats=%+v", calls.Load(), stats)
	}
	if err := w.Submit(func(context.Context) error { return nil }); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed submit: %v", err)
	}
}

func TestQueueIsBoundedAndShutdownDrains(t *testing.T) {
	w := New(Options{Capacity: 1, Workers: 1})
	started, unblock := make(chan struct{}), make(chan struct{})
	if err := w.Submit(func(context.Context) error { close(started); <-unblock; return nil }); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := w.Submit(func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if err := w.Submit(func(context.Context) error { return nil }); !errors.Is(err, ErrSaturated) {
		t.Fatalf("overflow: %v", err)
	}
	if stats := w.Stats(); stats.Pending != 2 || stats.Rejected != 1 {
		t.Fatalf("stats=%+v", stats)
	}
	close(unblock)
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if stats := w.Stats(); stats.Completed != 2 || stats.Pending != 0 {
		t.Fatalf("stats=%+v", stats)
	}
}

func TestFailedJobsAreObservable(t *testing.T) {
	w := New(Options{Attempts: 1})
	failure := errors.New("offline")
	if err := w.Write(context.Background(), func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatalf("write: %v", err)
	}
	if err := w.Close(context.Background()); !errors.Is(err, ErrFailed) {
		t.Fatalf("close: %v", err)
	}
	if w.Stats().Failed != 1 {
		t.Fatal("failure not counted")
	}
}

func TestShutdownDeadlineCancelsWorkers(t *testing.T) {
	w := New(Options{Attempts: 3, RetryBackoff: time.Hour})
	started := make(chan struct{})
	if err := w.Submit(func(ctx context.Context) error { close(started); <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("close: %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := w.Close(ctx); !errors.Is(err, ErrFailed) {
		t.Fatalf("drain: %v", err)
	}
}

func TestWriteCancellationAndNilWriter(t *testing.T) {
	w := New(Options{Attempts: 99, AttemptTimeout: time.Millisecond, RetryBackoff: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := w.Write(ctx, func(ctx context.Context) error { return ctx.Err() }); !errors.Is(err, context.Canceled) {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close(context.Background())
	var empty *Writer
	if err := empty.Submit(nil); !errors.Is(err, ErrClosed) {
		t.Fatal(err)
	}
	if err := empty.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if empty.Stats() != (Stats{}) {
		t.Fatal("nonzero nil stats")
	}
}
