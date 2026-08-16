// Package database owns the PostgreSQL connection pool and schema migrations.
package database

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/crypto-market-advisor/advisor/internal/config"
	"github.com/crypto-market-advisor/advisor/internal/logging"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// DB wraps the pgx pool together with its logger.
type DB struct {
	Pool *pgxpool.Pool
	log  *slog.Logger
}

// Connect opens the pool and verifies connectivity.
func Connect(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger) (*DB, error) {
	log := logging.For(logger, logging.CategoryDatabase)

	poolCfg, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	// Only override what the caller actually configured: a zero value here
	// would disable the pgx default and expire connections immediately.
	if cfg.MaxConns > 0 {
		poolCfg.MaxConns = cfg.MaxConns
	}
	if cfg.MinConns > 0 {
		poolCfg.MinConns = cfg.MinConns
	}
	if cfg.MaxConnLifetime > 0 {
		poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.ConnectTimeout > 0 {
		poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	log.Info("database connected", slog.Int("max_conns", int(cfg.MaxConns)))
	return &DB{Pool: pool, log: log}, nil
}

// ConnectWithRetry retries the initial connection, which matters on first
// `docker compose up` when Postgres is still starting.
func ConnectWithRetry(ctx context.Context, cfg config.DatabaseConfig, logger *slog.Logger, attempts int, wait time.Duration) (*DB, error) {
	var lastErr error
	for i := 0; i < attempts; i++ {
		db, err := Connect(ctx, cfg, logger)
		if err == nil {
			return db, nil
		}
		lastErr = err
		logging.For(logger, logging.CategoryDatabase).Warn("database not ready, retrying",
			slog.Int("attempt", i+1),
			slog.String("error", err.Error()),
		)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
	}
	return nil, fmt.Errorf("database unavailable after %d attempts: %w", attempts, lastErr)
}

// Close releases the pool.
func (d *DB) Close() {
	if d.Pool != nil {
		d.Pool.Close()
	}
}

// Ping verifies the connection is alive.
func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

// Migrate applies all pending migrations using the embedded SQL files.
func Migrate(databaseURL string, logger *slog.Logger) error {
	log := logging.For(logger, logging.CategoryDatabase)

	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("open migration source: %w", err)
	}

	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL(databaseURL))
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() {
		srcErr, dbErr := m.Close()
		if srcErr != nil {
			log.Warn("migration source close failed", slog.String("error", srcErr.Error()))
		}
		if dbErr != nil {
			log.Warn("migration db close failed", slog.String("error", dbErr.Error()))
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	version, dirty, err := m.Version()
	if err != nil && !errors.Is(err, migrate.ErrNilVersion) {
		return fmt.Errorf("read migration version: %w", err)
	}
	log.Info("migrations applied", slog.Uint64("version", uint64(version)), slog.Bool("dirty", dirty))
	return nil
}

// MigrateDown rolls back every migration; used by tests and `make migrate-down`.
func MigrateDown(databaseURL string) error {
	src, err := iofs.New(migrationFS, "migrations")
	if err != nil {
		return fmt.Errorf("open migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, migrateURL(databaseURL))
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("rollback migrations: %w", err)
	}
	return nil
}

// migrateURL adapts a standard postgres URL to the migrate pgx/v5 driver.
func migrateURL(u string) string {
	if len(u) > 11 && u[:11] == "postgres://" {
		return "pgx5://" + u[11:]
	}
	if len(u) > 13 && u[:13] == "postgresql://" {
		return "pgx5://" + u[13:]
	}
	return u
}

var _ = migratepgx.Postgres{} // keep the driver registered
