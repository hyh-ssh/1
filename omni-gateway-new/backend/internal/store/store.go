package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"time"

	_ "github.com/lib/pq"
	"xorm.io/xorm"
	"xorm.io/xorm/core"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

var errXORMNotConfigured = errors.New("xorm engine is not configured")

func normalizeContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Store wraps an XORM engine and provides typed data access.
type Store struct {
	engine *xorm.Engine
	pool   *xormPool
}

// New connects to PostgreSQL and returns a Store.
func New(ctx context.Context, dbURL string) (*Store, error) {
	ctx = normalizeContext(ctx)
	engine, err := xorm.NewEngine("postgres", dbURL)
	if err != nil {
		return nil, fmt.Errorf("create xorm engine: %w", err)
	}
	engine.SetMaxOpenConns(20)
	engine.SetMaxIdleConns(2)
	engine.SetConnMaxLifetime(time.Hour)
	engine.SetConnMaxIdleTime(5 * time.Minute)
	if err := engine.PingContext(ctx); err != nil {
		_ = engine.Close()
		return nil, fmt.Errorf("ping db: %w", err)
	}
	return &Store{engine: engine, pool: &xormPool{engine: engine}}, nil
}

// Pool exposes the XORM-backed query facade for readiness checks and
// integration code that needs a context-aware ping.
func (s *Store) Pool() *xormPool {
	if s == nil {
		return nil
	}
	return s.pool
}

// Close closes the XORM engine.
func (s *Store) Close() {
	if s != nil && s.engine != nil {
		_ = s.engine.Close()
	}
}

// Migrate applies all pending SQL migrations under a transaction-scoped lock.
func (s *Store) Migrate(ctx context.Context) error {
	if s == nil || s.engine == nil {
		return errXORMNotConfigured
	}
	ctx = normalizeContext(ctx)
	session := s.engine.NewSession().Context(ctx)
	defer session.Close()
	if err := session.Begin(); err != nil {
		return fmt.Errorf("begin migration transaction: %w", err)
	}
	defer session.Rollback()
	if _, err := session.Exec(`SELECT pg_advisory_xact_lock(727001)`); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if _, err := session.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version TEXT PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := migrationNames(migrationsFS)
	if err != nil {
		return err
	}

	for _, name := range names {
		var exists bool
		result, err := session.SQL(`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version=$1)`, name).Query()
		if err != nil {
			return fmt.Errorf("check migration %s: %w", name, err)
		}
		if len(result) > 0 {
			value := result[0]["exists"]
			exists = string(value) == "t" || string(value) == "true"
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := session.Exec(string(sqlBytes)); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := session.Exec(
			`INSERT INTO schema_migrations(version) VALUES($1)`, name); err != nil {
			return fmt.Errorf("record migration %s: %w", name, err)
		}
	}
	if err := session.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// migrationNames returns the SQL migration filenames in fsys, in the order they
// must be applied. AppleDouble sidecar files are skipped: some archive and
// unzip tools on macOS materialise a binary "._name" companion next to every
// file, and because such a name still ends in ".sql" the //go:embed wildcard
// picks it up (Go's '.'/'_' exclusion only applies to directory expansion, not
// to explicit wildcards). Applying one aborts startup with a confusing
// "invalid message format" error, and because '.' sorts before '0' it would run
// before the real migration.
func migrationNames(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || strings.HasPrefix(name, "._") || !strings.HasSuffix(name, ".sql") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// xormPool keeps the context-aware, row-oriented surface used by the Store
// implementation while routing every operation through XORM's core DB.
type xormPool struct{ engine *xorm.Engine }

func (p *xormPool) db() (*core.DB, error) {
	if p == nil || p.engine == nil {
		return nil, errXORMNotConfigured
	}
	return p.engine.DB(), nil
}

func (p *xormPool) Ping(ctx context.Context) error {
	ctx = normalizeContext(ctx)
	if _, err := p.db(); err != nil {
		return err
	}
	return p.engine.PingContext(ctx)
}
func (p *xormPool) Close() {
	if p != nil && p.engine != nil {
		_ = p.engine.Close()
	}
}
func (p *xormPool) Query(ctx context.Context, query string, args ...any) (*core.Rows, error) {
	ctx = normalizeContext(ctx)
	db, err := p.db()
	if err != nil {
		return nil, err
	}
	return db.QueryContext(ctx, query, args...)
}
func (p *xormPool) QueryRow(ctx context.Context, query string, args ...any) *core.Row {
	ctx = normalizeContext(ctx)
	db, err := p.db()
	if err != nil {
		return core.ErrorRow(err)
	}
	return db.QueryRowContext(ctx, query, args...)
}
func (p *xormPool) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	ctx = normalizeContext(ctx)
	db, err := p.db()
	if err != nil {
		return nil, err
	}
	return db.ExecContext(ctx, query, args...)
}
func (p *xormPool) Begin(ctx context.Context) (*xormTx, error) {
	ctx = normalizeContext(ctx)
	db, err := p.db()
	if err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &xormTx{tx: tx}, nil
}

type xormTx struct{ tx *core.Tx }

func (t *xormTx) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(normalizeContext(ctx), query, args...)
}
func (t *xormTx) Query(ctx context.Context, query string, args ...any) (*core.Rows, error) {
	return t.tx.QueryContext(normalizeContext(ctx), query, args...)
}
func (t *xormTx) QueryRow(ctx context.Context, query string, args ...any) *core.Row {
	return t.tx.QueryRowContext(normalizeContext(ctx), query, args...)
}
func (t *xormTx) Commit(_ context.Context) error   { return t.tx.Commit() }
func (t *xormTx) Rollback(_ context.Context) error { return t.tx.Rollback() }
