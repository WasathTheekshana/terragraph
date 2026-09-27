// Package store persists scans and serves the module usage queries, backed
// by Postgres.
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned when a requested project or module doesn't exist.
var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool}
}

// Migrate applies pending migrations. A Postgres advisory lock makes it safe
// for several server replicas to start at once.
func (s *Store) Migrate(ctx context.Context) error {
	return s.migrateTo(ctx, 0)
}

// migrateTo applies migrations up to version, or all of them when version is 0.
func (s *Store) migrateTo(ctx context.Context, version int64) error {
	fsys, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}

	db := stdlib.OpenDBFromPool(s.pool)
	defer db.Close()

	provider, err := goose.NewProvider(goose.DialectPostgres, db, fsys, goose.WithSessionLocker(locker))
	if err != nil {
		return err
	}
	if version == 0 {
		_, err = provider.Up(ctx)
	} else {
		_, err = provider.UpTo(ctx, version)
	}
	if err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	return s.pool.Ping(ctx)
}
