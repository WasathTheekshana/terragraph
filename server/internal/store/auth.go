package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

type User struct {
	ID    int64
	Email string
	Name  string
}

type Session struct {
	User      User
	IsAdmin   bool
	CSRFToken string
	ExpiresAt time.Time
}

type APIToken struct {
	ID           int64      `json:"id"`
	Name         string     `json:"name"`
	Prefix       string     `json:"prefix"`
	CanRead      bool       `json:"can_read"`
	CanIngest    bool       `json:"can_ingest"`
	RepoPatterns []string   `json:"repo_patterns"`
	CreatedBy    string     `json:"created_by"`
	CreatedAt    time.Time  `json:"created_at"`
	LastUsedAt   *time.Time `json:"last_used_at"`
	ExpiresAt    *time.Time `json:"expires_at"`
	RevokedAt    *time.Time `json:"revoked_at"`
}

type NewAPIToken struct {
	Name         string
	Hash         []byte
	Prefix       string
	CanRead      bool
	CanIngest    bool
	RepoPatterns []string
	CreatedBy    *int64
	ExpiresAt    *time.Time
}

// UpsertUser records a sign-in, keyed by the identity provider's subject.
func (s *Store) UpsertUser(ctx context.Context, subject, email, name string) (User, error) {
	u := User{Email: email, Name: name}
	err := s.pool.QueryRow(ctx, `
		INSERT INTO users (subject, email, name) VALUES ($1, $2, $3)
		ON CONFLICT (subject) DO UPDATE SET email = EXCLUDED.email, name = EXCLUDED.name, last_login_at = now()
		RETURNING id`, subject, email, name).Scan(&u.ID)
	if err != nil {
		return User{}, fmt.Errorf("upserting user: %w", err)
	}
	return u, nil
}

func (s *Store) CreateSession(ctx context.Context, idHash []byte, userID int64, isAdmin bool, csrf string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id_hash, user_id, is_admin, csrf_token, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		idHash, userID, isAdmin, csrf, expiresAt)
	if err != nil {
		return fmt.Errorf("creating session: %w", err)
	}
	return nil
}

// SessionByHash returns an unexpired session, or ErrNotFound.
func (s *Store) SessionByHash(ctx context.Context, idHash []byte, now time.Time) (Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx, `
		SELECT u.id, u.email, u.name, s.is_admin, s.csrf_token, s.expires_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.id_hash = $1 AND s.expires_at > $2`, idHash, now,
	).Scan(&sess.User.ID, &sess.User.Email, &sess.User.Name, &sess.IsAdmin, &sess.CSRFToken, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("looking up session: %w", err)
	}
	return sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, idHash []byte) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE id_hash = $1`, idHash); err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

func (s *Store) CreateLoginAttempt(ctx context.Context, state, verifier, nonce, returnTo string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO login_attempts (state, verifier, nonce, return_to, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		state, verifier, nonce, returnTo, expiresAt)
	if err != nil {
		return fmt.Errorf("creating login attempt: %w", err)
	}
	return nil
}

// TakeLoginAttempt returns and deletes an unexpired sign-in attempt, so each
// can be completed only once.
func (s *Store) TakeLoginAttempt(ctx context.Context, state string, now time.Time) (verifier, nonce, returnTo string, err error) {
	var live bool
	err = s.pool.QueryRow(ctx, `
		DELETE FROM login_attempts WHERE state = $1
		RETURNING verifier, nonce, return_to, expires_at > $2`, state, now,
	).Scan(&verifier, &nonce, &returnTo, &live)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !live) {
		return "", "", "", ErrNotFound
	}
	if err != nil {
		return "", "", "", fmt.Errorf("taking login attempt: %w", err)
	}
	return verifier, nonce, returnTo, nil
}

// PruneAuth deletes expired sessions and sign-in attempts.
func (s *Store) PruneAuth(ctx context.Context, now time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= $1`, now); err != nil {
		return fmt.Errorf("pruning sessions: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM login_attempts WHERE expires_at <= $1`, now); err != nil {
		return fmt.Errorf("pruning login attempts: %w", err)
	}
	return nil
}

func (s *Store) CreateAPIToken(ctx context.Context, t NewAPIToken) (APIToken, error) {
	if t.RepoPatterns == nil {
		t.RepoPatterns = []string{}
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO api_tokens (name, token_hash, prefix, can_read, can_ingest, repo_patterns, created_by, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		t.Name, t.Hash, t.Prefix, t.CanRead, t.CanIngest, t.RepoPatterns, t.CreatedBy, t.ExpiresAt,
	).Scan(&id)
	if err != nil {
		return APIToken{}, fmt.Errorf("creating api token: %w", err)
	}
	return single(s.apiTokens(ctx, "WHERE t.id = $1", id))
}

func (s *Store) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	return s.apiTokens(ctx, "")
}

// APITokenByHash returns a token that is neither revoked nor expired, or
// ErrNotFound.
func (s *Store) APITokenByHash(ctx context.Context, hash []byte, now time.Time) (APIToken, error) {
	return single(s.apiTokens(ctx,
		"WHERE t.token_hash = $1 AND t.revoked_at IS NULL AND (t.expires_at IS NULL OR t.expires_at > $2)", hash, now))
}

func (s *Store) apiTokens(ctx context.Context, where string, args ...any) ([]APIToken, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.name, t.prefix, t.can_read, t.can_ingest, t.repo_patterns, COALESCE(u.email, ''),
			t.created_at, t.last_used_at, t.expires_at, t.revoked_at
		FROM api_tokens t
		LEFT JOIN users u ON u.id = t.created_by
		`+where+`
		ORDER BY t.revoked_at IS NOT NULL, t.created_at DESC`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying api tokens: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (APIToken, error) {
		var t APIToken
		err := row.Scan(&t.ID, &t.Name, &t.Prefix, &t.CanRead, &t.CanIngest, &t.RepoPatterns, &t.CreatedBy,
			&t.CreatedAt, &t.LastUsedAt, &t.ExpiresAt, &t.RevokedAt)
		return t, err
	})
}

// TouchAPIToken records a token's use, at most once a minute so busy
// pipelines don't write on every request.
func (s *Store) TouchAPIToken(ctx context.Context, id int64, now time.Time) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE api_tokens SET last_used_at = $2
		WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $2::timestamptz - interval '1 minute')`, id, now)
	if err != nil {
		return fmt.Errorf("touching api token: %w", err)
	}
	return nil
}

// RevokeAPIToken revokes a token; revoking it again is a no-op.
func (s *Store) RevokeAPIToken(ctx context.Context, id int64, now time.Time) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_tokens SET revoked_at = COALESCE(revoked_at, $2) WHERE id = $1`, id, now)
	if err != nil {
		return fmt.Errorf("revoking api token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
