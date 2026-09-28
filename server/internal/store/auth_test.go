package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestUsersAndSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	u, err := s.UpsertUser(ctx, "https://idp|123", "alice@acme.io", "Alice")
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.UpsertUser(ctx, "https://idp|123", "alice@acme.io", "Alice Smith")
	if err != nil || again.ID != u.ID {
		t.Fatalf("second sign-in = %+v, %v; want the same user", again, err)
	}

	hash := []byte("session-hash")
	if err := s.CreateSession(ctx, hash, u.ID, true, "csrf-1", t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	sess, err := s.SessionByHash(ctx, hash, t0)
	if err != nil || sess.User.Email != "alice@acme.io" || sess.User.Name != "Alice Smith" || !sess.IsAdmin || sess.CSRFToken != "csrf-1" {
		t.Errorf("session = %+v, %v", sess, err)
	}
	if _, err := s.SessionByHash(ctx, hash, t0.Add(2*time.Hour)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired session: %v, want ErrNotFound", err)
	}
	if err := s.DeleteSession(ctx, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionByHash(ctx, hash, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted session: %v, want ErrNotFound", err)
	}
}

func TestLoginAttemptsAreSingleUse(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.CreateLoginAttempt(ctx, "state-1", "verifier", "nonce", "/modules", t0.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	v, n, ret, err := s.TakeLoginAttempt(ctx, "state-1", t0)
	if err != nil || v != "verifier" || n != "nonce" || ret != "/modules" {
		t.Fatalf("take = %q %q %q %v", v, n, ret, err)
	}
	if _, _, _, err := s.TakeLoginAttempt(ctx, "state-1", t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("second take: %v, want ErrNotFound", err)
	}

	if err := s.CreateLoginAttempt(ctx, "state-2", "v", "n", "/", t0); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := s.TakeLoginAttempt(ctx, "state-2", t0.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired attempt: %v, want ErrNotFound", err)
	}
}

func TestPruneAuth(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "sub", "a@b.c", "")
	_ = s.CreateSession(ctx, []byte("old"), u.ID, false, "c", t0)
	_ = s.CreateSession(ctx, []byte("new"), u.ID, false, "c", t0.Add(time.Hour))
	_ = s.CreateLoginAttempt(ctx, "old", "v", "n", "/", t0)

	if err := s.PruneAuth(ctx, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var sessions, attempts int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM sessions`).Scan(&sessions)
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM login_attempts`).Scan(&attempts)
	if sessions != 1 || attempts != 0 {
		t.Errorf("after prune: %d sessions, %d attempts; want 1 and 0", sessions, attempts)
	}
}

func TestAPITokens(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	u, _ := s.UpsertUser(ctx, "sub", "alice@acme.io", "Alice")
	expires := t0.Add(24 * time.Hour)

	tok, err := s.CreateAPIToken(ctx, NewAPIToken{
		Name: "ci", Hash: []byte("hash-ci"), Prefix: "tg_abc", CanIngest: true,
		RepoPatterns: []string{"github.com/acme/*"}, CreatedBy: &u.ID, ExpiresAt: &expires,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tok.Name != "ci" || tok.CreatedBy != "alice@acme.io" || tok.CanRead || !tok.CanIngest || len(tok.RepoPatterns) != 1 {
		t.Errorf("created = %+v", tok)
	}
	if _, err := s.CreateAPIToken(ctx, NewAPIToken{Name: "reader", Hash: []byte("hash-r"), Prefix: "tg_def", CanRead: true}); err != nil {
		t.Fatal(err)
	}

	got, err := s.APITokenByHash(ctx, []byte("hash-ci"), t0)
	if err != nil || got.ID != tok.ID {
		t.Fatalf("by hash = %+v, %v", got, err)
	}
	if _, err := s.APITokenByHash(ctx, []byte("hash-ci"), expires.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Errorf("expired token: %v, want ErrNotFound", err)
	}
	if _, err := s.APITokenByHash(ctx, []byte("nope"), t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: %v, want ErrNotFound", err)
	}

	if err := s.TouchAPIToken(ctx, tok.ID, t0); err != nil {
		t.Fatal(err)
	}
	_ = s.TouchAPIToken(ctx, tok.ID, t0.Add(30*time.Second))
	got, _ = s.APITokenByHash(ctx, []byte("hash-ci"), t0)
	if got.LastUsedAt == nil || !got.LastUsedAt.Equal(t0) {
		t.Errorf("last used = %v, want %v (touches within a minute are skipped)", got.LastUsedAt, t0)
	}

	if err := s.RevokeAPIToken(ctx, tok.ID, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeAPIToken(ctx, tok.ID, t0.Add(time.Hour)); err != nil {
		t.Errorf("revoking twice: %v", err)
	}
	if _, err := s.APITokenByHash(ctx, []byte("hash-ci"), t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token: %v, want ErrNotFound", err)
	}
	if err := s.RevokeAPIToken(ctx, 999, t0); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking unknown token: %v, want ErrNotFound", err)
	}

	all, err := s.ListAPITokens(ctx)
	if err != nil || len(all) != 2 || all[0].Name != "reader" || all[1].RevokedAt == nil || !all[1].RevokedAt.Equal(t0) {
		t.Errorf("list = %+v, %v; want active tokens first, revoked keeping its first revocation time", all, err)
	}
}
