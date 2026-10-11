// Command server starts the LLM gateway monolith.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/zjvtit/omni-gateway/internal/billing"
	"github.com/zjvtit/omni-gateway/internal/config"
	gatewaycrypto "github.com/zjvtit/omni-gateway/internal/crypto"
	"github.com/zjvtit/omni-gateway/internal/gateway"
	"github.com/zjvtit/omni-gateway/internal/httpapi"
	"github.com/zjvtit/omni-gateway/internal/model"
	"github.com/zjvtit/omni-gateway/internal/policy"
	"github.com/zjvtit/omni-gateway/internal/store"
	"github.com/zjvtit/omni-gateway/internal/upstream"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Printf("configuration error: %v", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, cfg); err != nil {
		log.Printf("server error: %v", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, cfg *config.Config) error {
	if cfg == nil {
		return errors.New("nil configuration")
	}
	// Validate the master key before opening a database connection. The cipher
	// is retained for decrypting provider credentials during request routing.
	cipher, err := gatewaycrypto.NewRotatingCipher(cfg.MasterKey, cfg.PreviousMasterKeys)
	if err != nil {
		return fmt.Errorf("initialize master cipher: %w", err)
	}

	db, err := store.New(ctx, cfg.DBURL)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	if len(cfg.PreviousMasterKeys) > 0 {
		if err := db.RotateSecrets(ctx, cipher); err != nil {
			return fmt.Errorf("rotate secrets: %w", err)
		}
		log.Print("credential and content key rotation completed")
	}
	// The environment seeds the primary provider exactly once, and only into a
	// missing row: a row an administrator configured - or deliberately cleared -
	// always wins, so a restart cannot bring the environment values back. A
	// failure is logged and stepped over rather than fatal, because password
	// login does not depend on OIDC; the error carries no secret material.
	if seeded, err := seedOIDCProviderFromEnv(ctx, db, cipher, cfg); err != nil {
		log.Printf("oidc provider environment seed failed: %v", err)
	} else if seeded {
		log.Print("oidc provider seeded from the GATEWAY_OIDC_* environment parameters")
	}
	if err := ensureInitialAdmin(ctx, db, cfg); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	settlement := billing.NewService(db, db)
	policies := policy.NewService(db)
	upstreamClient := upstream.NewClient(cfg.AllowPrivateUpstreams)
	defer upstreamClient.CloseIdleConnections()
	service := gateway.NewServiceWithDependencies(db, cipher, upstreamClient, db, settlement, policies)
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := service.Close(closeCtx); err != nil {
			log.Printf("gateway persistence drain: %v", err)
		}
		if err := settlement.Close(closeCtx); err != nil {
			log.Printf("billing persistence drain: %v", err)
		}
	}()
	service.SetContentLogging(db, cipher, cfg.ContentLogMaxBytes, cfg.ContentLoggingEnabled)
	requestCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()

	// Runtime view of the administrator configured provider. The administrator
	// endpoints invalidate it after every write, so console changes apply
	// without a restart.
	oidcProviders := httpapi.NewOIDCProviderSource(db, cipher.Decrypt)

	server := &http.Server{
		Addr: cfg.ListenAddr,
		Handler: httpapi.NewHandlerWithGatewayAndCipher(db.Pool(), db, db, service, httpapi.AuthConfig{SessionTTL: cfg.SessionTTL, CookieSecure: cfg.CookieSecure, UpstreamClient: upstreamClient, OIDCProvider: oidcProviders, RuntimeStats: func() any {
			return map[string]any{
				"records": service.PersistenceStats(),
				"billing": settlement.PersistenceStats(),
				"gateway": service.RuntimeCounters(),
			}
		}}, cipher),
		BaseContext:       func(net.Listener) context.Context { return requestCtx },
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}
	go runMaintenance(ctx, db)
	defer listener.Close()

	serveErr := make(chan error, 1)
	go func() {
		log.Printf("omni-gateway listening on %s", cfg.ListenAddr)
		serveErr <- server.Serve(listener)
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		shutdownTimeout := cfg.ShutdownTimeout
		if shutdownTimeout <= 0 {
			shutdownTimeout = 10 * time.Second
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			cancelRequests()
			_ = server.Close()
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		if err := service.Close(shutdownCtx); err != nil {
			log.Printf("gateway persistence drain: %v", err)
		}
		if err := settlement.Close(shutdownCtx); err != nil {
			log.Printf("billing persistence drain: %v", err)
		}
		return nil
	}
}

func runMaintenance(ctx context.Context, db *store.Store) {
	if db == nil {
		return
	}
	ticker := time.NewTicker(15 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			maintenanceCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, _ = db.DeleteExpiredSessions(maintenanceCtx)
			_ = db.RefreshAggregates(maintenanceCtx, now.Add(-48*time.Hour))
			cancel()
		}
	}
}

type adminBootstrapStore interface {
	CountUsers(context.Context) (int64, error)
	CreateUser(context.Context, *model.User) (int64, error)
}

func ensureInitialAdmin(ctx context.Context, db adminBootstrapStore, cfg *config.Config) error {
	count, err := db.CountUsers(ctx)
	if err != nil {
		return err
	}
	if count > 0 {
		return nil
	}

	username := strings.TrimSpace(cfg.InitialAdminUser)
	if username == "" {
		return errors.New("GATEWAY_ADMIN_USER must not be empty when bootstrapping the first user")
	}
	if cfg.InitialAdminPassword == "" {
		return errors.New("GATEWAY_ADMIN_PASSWORD is required when bootstrapping the first user")
	}
	passwordHash, err := gatewaycrypto.HashPassword(cfg.InitialAdminPassword)
	if err != nil {
		return fmt.Errorf("hash initial admin password: %w", err)
	}

	_, err = db.CreateUser(ctx, &model.User{
		Username:     username,
		DisplayName:  username,
		Role:         model.RoleAdmin,
		PasswordHash: passwordHash,
		Active:       true,
	})
	return err
}

// oidcProviderSeeder is the store surface the startup seed needs.
type oidcProviderSeeder interface {
	SeedOIDCProviderFromEnv(context.Context, string, string, string, string, string) (bool, error)
}

// seedOIDCProviderFromEnv imports GATEWAY_OIDC_* into the singleton provider row
// the first time the gateway starts against a database. It encrypts the client
// secret before it touches the store, so the plaintext never reaches the
// database, and it lets store.SeedOIDCProviderFromEnv decide whether the row is
// still missing - that SQL is what makes the seed a one-time event that cannot
// revive values an administrator configured or cleared. The returned error
// never contains the secret, so the caller logs it verbatim.
func seedOIDCProviderFromEnv(ctx context.Context, db oidcProviderSeeder, cipher *gatewaycrypto.Cipher, cfg *config.Config) (bool, error) {
	if cfg == nil {
		return false, errors.New("nil configuration")
	}
	issuer := strings.TrimSpace(cfg.OIDCIssuer)
	clientID := strings.TrimSpace(cfg.OIDCClientID)
	secret := cfg.OIDCClientSecret
	redirectURL := strings.TrimSpace(cfg.OIDCRedirectURL)
	if issuer == "" && clientID == "" && secret == "" && redirectURL == "" {
		// Nothing configured: an OIDC-free gateway is a supported shape.
		return false, nil
	}
	if issuer == "" || clientID == "" || secret == "" || redirectURL == "" {
		return false, errors.New("GATEWAY_OIDC_* is incomplete: issuer, client_id, client_secret and redirect_url must all be set; nothing was seeded")
	}
	if cipher == nil {
		return false, errors.New("the master cipher is required to seed the OIDC client secret")
	}
	ciphertext, err := cipher.Encrypt(secret)
	if err != nil {
		// Encryption failures are nonce/entropy failures; the wrapped error
		// never echoes its input.
		return false, fmt.Errorf("encrypt the OIDC client secret: %w", err)
	}
	seeded, err := db.SeedOIDCProviderFromEnv(ctx, issuer, clientID, ciphertext, redirectURL, strings.TrimSpace(cfg.OIDCDisplayName))
	if err != nil {
		return false, err
	}
	return seeded, nil
}
