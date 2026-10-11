package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	neturl "net/url"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"github.com/zjvtit/omni-gateway/internal/config"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/store"
)

type fakeAdminStore struct {
	count     int64
	countErr  error
	createErr error
	created   *model.User
}

func (s *fakeAdminStore) CountUsers(context.Context) (int64, error) {
	return s.count, s.countErr
}

func (s *fakeAdminStore) CreateUser(_ context.Context, user *model.User) (int64, error) {
	if s.createErr != nil {
		return 0, s.createErr
	}
	copy := *user
	s.created = &copy
	return 1, nil
}

func TestEnsureInitialAdminCreatesOnlyWhenDatabaseIsEmpty(t *testing.T) {
	cfg := &config.Config{
		InitialAdminUser:     " admin ",
		InitialAdminPassword: "password",
	}
	store := &fakeAdminStore{}

	if err := ensureInitialAdmin(context.Background(), store, cfg); err != nil {
		t.Fatalf("ensureInitialAdmin returned error: %v", err)
	}
	if store.created == nil {
		t.Fatal("initial admin was not created")
	}
	if store.created.Username != "admin" || store.created.DisplayName != "admin" || store.created.Role != model.RoleAdmin || !store.created.Active {
		t.Fatalf("unexpected initial admin: %#v", store.created)
	}
	if store.created.PasswordHash == "" || store.created.PasswordHash == cfg.InitialAdminPassword {
		t.Fatal("initial admin password was not hashed")
	}

	existing := &fakeAdminStore{count: 1}
	if err := ensureInitialAdmin(context.Background(), existing, cfg); err != nil {
		t.Fatalf("existing-user bootstrap returned error: %v", err)
	}
	if existing.created != nil {
		t.Fatal("bootstrap created an admin despite existing users")
	}
}

func TestEnsureInitialAdminValidatesEmptyDatabaseConfiguration(t *testing.T) {
	cases := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "missing username",
			cfg:  &config.Config{InitialAdminPassword: "password"},
			want: "GATEWAY_ADMIN_USER",
		},
		{
			name: "missing password",
			cfg:  &config.Config{InitialAdminUser: "admin"},
			want: "GATEWAY_ADMIN_PASSWORD",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeAdminStore{}
			err := ensureInitialAdmin(context.Background(), store, tc.cfg)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got error %v, want it to contain %q", err, tc.want)
			}
			if store.created != nil {
				t.Fatal("invalid configuration created a user")
			}
		})
	}
}

func TestEnsureInitialAdminPropagatesStoreErrors(t *testing.T) {
	countErr := errors.New("count failed")
	if err := ensureInitialAdmin(context.Background(), &fakeAdminStore{countErr: countErr}, &config.Config{}); !errors.Is(err, countErr) {
		t.Fatalf("got error %v, want %v", err, countErr)
	}

	createErr := errors.New("create failed")
	if err := ensureInitialAdmin(context.Background(), &fakeAdminStore{createErr: createErr}, &config.Config{InitialAdminUser: "admin", InitialAdminPassword: "password"}); !errors.Is(err, createErr) {
		t.Fatalf("got error %v, want %v", err, createErr)
	}
}

// masterKeyHex is a valid 32-byte AES-256-GCM key in hex form, matching what
// GATEWAY_MASTER_KEY accepts.
var masterKeyHex = strings.Repeat("ab", 32)

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	if err := run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "nil configuration") {
		t.Fatalf("got error %v, want nil-configuration error", err)
	}

	badKey := &config.Config{MasterKey: "not-a-valid-key"}
	if err := run(context.Background(), badKey); err == nil || !strings.Contains(err.Error(), "initialize master cipher") {
		t.Fatalf("got error %v, want master cipher error", err)
	}
}

func TestRunReportsUnreachableDatabase(t *testing.T) {
	cfg := &config.Config{
		MasterKey:  masterKeyHex,
		DBURL:      "postgres://user:pass@127.0.0.1:1/does_not_exist?sslmode=disable&connect_timeout=1",
		ListenAddr: "127.0.0.1:0",
	}
	if err := run(context.Background(), cfg); err == nil {
		t.Fatal("expected an error when the database is unreachable")
	} else if !strings.Contains(err.Error(), "ping db") {
		t.Fatalf("got error %v, want a database ping failure", err)
	}
}

// serverTestDatabase provisions an isolated PostgreSQL schema and returns a DSN
// scoped to it. The test is skipped when TEST_DATABASE_URL is unset so the
// package still builds and tests in environments without PostgreSQL.
func serverTestDatabase(t *testing.T) string {
	t.Helper()
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("TEST_DATABASE_URL is required for the server integration test")
	}
	ctx := context.Background()
	admin, err := sql.Open("postgres", raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := admin.PingContext(ctx); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := fmt.Sprintf("server_test_%d", time.Now().UnixNano())
	quoted := `"` + strings.ReplaceAll(schema, `"`, `""`) + `"`
	if _, err := admin.ExecContext(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		admin.Close()
	})
	u, err := neturl.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("options", "-csearch_path="+schema)
	u.RawQuery = query.Encode()
	return u.String()
}

func TestRunServesRequestsAndShutsDownGracefully(t *testing.T) {
	dsn := serverTestDatabase(t)
	addr := freeListenAddr(t)
	cfg := &config.Config{
		MasterKey:            masterKeyHex,
		DBURL:                dsn,
		ListenAddr:           addr,
		InitialAdminUser:     "admin",
		InitialAdminPassword: "password",
		ShutdownTimeout:      5 * time.Second,
		SessionTTL:           time.Hour,
		ContentLogMaxBytes:   1024,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, cfg) }()

	// The server must reach the point of accepting connections before
	// cancellation is meaningful: that covers migrations, admin bootstrap and
	// the listener coming up, not just the shutdown path.
	if err := waitForListener(addr, 30*time.Second); err != nil {
		cancel()
		t.Fatalf("server never accepted a connection on %s: %v", addr, err)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned error after graceful shutdown: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after context cancellation")
	}
}

func TestRunMaintenanceReturnsOnNilStoreAndCancelledContext(t *testing.T) {
	// A nil store is a no-op.
	runMaintenance(context.Background(), nil)

	// A non-nil store with an already-cancelled context must return without
	// waiting for the maintenance ticker, which fires every 15 minutes. Passing
	// a nil store here would return at the nil guard and never reach the select.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		runMaintenance(ctx, &store.Store{})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("runMaintenance did not return for a cancelled context")
	}
}

// freeListenAddr reserves a loopback port and releases it so the server under
// test can bind it. Losing the race surfaces as a listen error inside run().
func freeListenAddr(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return addr
}

// waitForListener polls addr until the server accepts a TCP connection.
func waitForListener(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(25 * time.Millisecond)
	}
	return lastErr
}
