package httpapi

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/zjvtit/omni-gateway/internal/model"
)

type loginState struct {
	failures         int
	blocked, touched time.Time
}
type loginLimiter struct {
	mu     sync.Mutex
	states map[string]loginState
	active int
}

// begin limits expensive Argon2 work and uses the socket peer, never untrusted
// forwarding headers. Five attempts trigger progressive 1..60 second delays.
func (l *loginLimiter) begin(peer string, now time.Time) (func(bool), time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.states == nil {
		l.states = make(map[string]loginState)
	}
	for key, state := range l.states {
		if now.Sub(state.touched) > 15*time.Minute {
			delete(l.states, key)
		}
	}
	state := l.states[peer]
	if state.blocked.After(now) {
		return nil, state.blocked.Sub(now)
	}
	if l.active >= 4 || (len(l.states) >= 10000 && state.touched.IsZero()) {
		return nil, time.Second
	}
	l.active++
	state.touched = now
	l.states[peer] = state
	var once sync.Once
	return func(success bool) {
		once.Do(func() {
			l.mu.Lock()
			defer l.mu.Unlock()
			l.active--
			state := l.states[peer]
			if success {
				delete(l.states, peer)
				return
			}
			state.failures++
			if state.failures >= 5 {
				state.blocked = now.Add(min(time.Duration(1<<uint(min(state.failures-5, 6)))*time.Second, time.Minute))
			}
			state.touched = now
			l.states[peer] = state
		})
	}, 0
}

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func writeLoginLimited(w http.ResponseWriter, wait time.Duration) {
	w.Header().Set("Retry-After", strconv.Itoa(max(1, int(wait.Seconds()+0.999))))
	writeError(w, http.StatusTooManyRequests, "authentication_error", "login_rate_limited", "too many login attempts")
}

func (h *authHandler) auditLogin(r *http.Request, username string, success bool) {
	if audit, ok := h.store.(interface {
		InsertAudit(context.Context, *model.AuditEntry) error
	}); ok {
		action := "login_failed"
		if success {
			action = "login_succeeded"
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), time.Second)
		defer cancel()
		// The attempted username is recorded even when authentication failed,
		// so an operator can attribute the attempt. No user id is available
		// for a failed or unknown account.
		result := "failure"
		if success {
			result = "success"
		}
		_ = audit.InsertAudit(ctx, &model.AuditEntry{Action: action, Resource: "session", ActorName: username, IP: peerIP(r), Result: result})
	}
}
