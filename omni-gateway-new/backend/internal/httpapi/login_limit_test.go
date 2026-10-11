package httpapi

import (
	"testing"
	"time"
)

func TestLoginLimiterBackoffAndRecovery(t *testing.T) {
	l := &loginLimiter{}
	now := time.Now()
	for i := 0; i < 5; i++ {
		done, _ := l.begin("peer", now)
		if done == nil {
			t.Fatal("blocked early")
		}
		done(false)
		done(false)
	}
	if done, delay := l.begin("peer", now); done != nil || delay != time.Second {
		t.Fatalf("delay=%v", delay)
	}
	done, _ := l.begin("peer", now.Add(time.Second))
	if done == nil {
		t.Fatal("not released")
	}
	done(true)
	if _, delay := l.begin("peer", now.Add(time.Second)); delay != 0 {
		t.Fatal("not reset")
	}
}

func TestLoginLimiterBoundsExpensiveWork(t *testing.T) {
	l := &loginLimiter{}
	now := time.Now()
	var finishes []func(bool)
	for i := 0; i < 4; i++ {
		done, _ := l.begin("peer", now)
		finishes = append(finishes, done)
	}
	if done, _ := l.begin("other", now); done != nil {
		t.Fatal("exceeded concurrency")
	}
	for _, done := range finishes {
		done(true)
	}
	if done, _ := l.begin("other", now); done == nil {
		t.Fatal("slot not released")
	} else {
		done(true)
	}
}
