package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/WasathTheekshana/terragraph/server/internal/report"
	"github.com/WasathTheekshana/terragraph/server/internal/source"
)

const (
	RunRunning   = "running"
	RunFinished  = "finished"
	RunCancelled = "cancelled"

	ItemPending = "pending"
	ItemDone    = "done"
	ItemFailed  = "failed"

	ItemKindProject    = "project"
	ItemKindModuleRepo = "module_repo"
)

var (
	// ErrRunClosed is returned when changing a run that is no longer running.
	ErrRunClosed = errors.New("run is no longer running")
	// ErrItemMismatch is returned when a report isn't for the run item it was
	// submitted to.
	ErrItemMismatch = errors.New("report does not match the run item")
)

type Run struct {
	ID         int64      `json:"id"`
	Label      string     `json:"label"`
	Status     string     `json:"status"`
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	FinishedAt *time.Time `json:"finished_at"`
	Total      int        `json:"total"`
	Pending    int        `json:"pending"`
	Done       int        `json:"done"`
	Failed     int        `json:"failed"`
}

type RunItem struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	RepoURL   string    `json:"repo_url"`
	Path      string    `json:"path"`
	Status    string    `json:"status"`
	Error     string    `json:"error"`
	ScanID    *int64    `json:"scan_id"`
	Applied   *bool     `json:"applied"`
	UpdatedAt time.Time `json:"updated_at"`
	// ProjectID or ModuleID is set once the item's scan is recorded.
	ProjectID *int64 `json:"project_id"`
	ModuleID  *int64 `json:"module_id"`
}

type NewRunItem struct {
	Kind    string `json:"kind"`
	RepoURL string `json:"repo_url"`
	Path    string `json:"path"`
}

// CreateRun records a run and its planned items. A repeated idempotencyKey
// returns the run it created first, so a retried request can't duplicate it.
func (s *Store) CreateRun(ctx context.Context, label, idempotencyKey string, items []NewRunItem) (Run, []RunItem, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Run{}, nil, err
	}
	defer tx.Rollback(ctx)

	var key *string
	if idempotencyKey != "" {
		key = &idempotencyKey
	}
	var id int64
	err = tx.QueryRow(ctx, `
		INSERT INTO runs (label, idempotency_key) VALUES ($1, $2)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id`, label, key).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.QueryRow(ctx, `SELECT id FROM runs WHERE idempotency_key = $1`, key).Scan(&id); err != nil {
			return Run{}, nil, fmt.Errorf("looking up run by idempotency key: %w", err)
		}
		return s.runWithItems(ctx, id)
	}
	if err != nil {
		return Run{}, nil, fmt.Errorf("inserting run: %w", err)
	}

	rows := make([][]any, len(items))
	for i, it := range items {
		rows[i] = []any{id, it.Kind, it.RepoURL, it.Path}
	}
	if _, err := tx.CopyFrom(ctx, pgx.Identifier{"run_items"}, []string{"run_id", "kind", "repo_url", "path"}, pgx.CopyFromRows(rows)); err != nil {
		return Run{}, nil, fmt.Errorf("inserting run items: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Run{}, nil, err
	}
	return s.runWithItems(ctx, id)
}

func (s *Store) runWithItems(ctx context.Context, id int64) (Run, []RunItem, error) {
	run, err := s.GetRun(ctx, id)
	if err != nil {
		return Run{}, nil, err
	}
	items, err := s.RunItems(ctx, id)
	if err != nil {
		return Run{}, nil, err
	}
	return run, items, nil
}

// IngestRunItem ingests a run item's report and marks the item done in one
// transaction. Submitting an item again returns the first result.
func (s *Store) IngestRunItem(ctx context.Context, runID, itemID int64, in Scan) (IngestResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return IngestResult{}, err
	}
	defer tx.Rollback(ctx)

	it, runStatus, err := lockItem(ctx, tx, runID, itemID)
	if err != nil {
		return IngestResult{}, err
	}
	if it.Status == ItemDone && it.ScanID != nil {
		return IngestResult{ScanID: *it.ScanID, Applied: it.Applied != nil && *it.Applied, Duplicate: true}, nil
	}
	if runStatus != RunRunning {
		return IngestResult{}, ErrRunClosed
	}
	if err := matchItem(it, in.Report); err != nil {
		return IngestResult{}, err
	}

	res, err := ingest(ctx, tx, in)
	if err != nil {
		return IngestResult{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE run_items SET status = 'done', error = '', scan_id = $2, applied = $3, updated_at = now()
		WHERE id = $1`, itemID, res.ScanID, res.Applied); err != nil {
		return IngestResult{}, fmt.Errorf("updating run item: %w", err)
	}
	if err := touchRun(ctx, tx, runID); err != nil {
		return IngestResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IngestResult{}, err
	}
	return res, nil
}

// FailRunItem records why an item couldn't be scanned. An item that already
// succeeded stays done.
func (s *Store) FailRunItem(ctx context.Context, runID, itemID int64, msg string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	it, runStatus, err := lockItem(ctx, tx, runID, itemID)
	if err != nil {
		return err
	}
	if it.Status == ItemDone {
		return nil
	}
	if runStatus != RunRunning {
		return ErrRunClosed
	}
	if _, err := tx.Exec(ctx, `UPDATE run_items SET status = 'failed', error = $2, updated_at = now() WHERE id = $1`, itemID, msg); err != nil {
		return fmt.Errorf("updating run item: %w", err)
	}
	if err := touchRun(ctx, tx, runID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// FinishRun closes a run as finished or cancelled. Closing it again with the
// same status is a no-op.
func (s *Store) FinishRun(ctx context.Context, runID int64, status string) (Run, error) {
	if status != RunFinished && status != RunCancelled {
		return Run{}, fmt.Errorf("invalid final run status %q", status)
	}
	var current string
	err := s.pool.QueryRow(ctx, `
		UPDATE runs SET status = $2, finished_at = now(), updated_at = now()
		WHERE id = $1 AND status = 'running'
		RETURNING status`, runID, status).Scan(&current)
	if errors.Is(err, pgx.ErrNoRows) {
		run, err := s.GetRun(ctx, runID)
		if err != nil {
			return Run{}, err
		}
		if run.Status != status {
			return Run{}, ErrRunClosed
		}
		return run, nil
	}
	if err != nil {
		return Run{}, fmt.Errorf("finishing run: %w", err)
	}
	return s.GetRun(ctx, runID)
}

func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	return s.runs(ctx, "", "LIMIT $1", limit)
}

func (s *Store) GetRun(ctx context.Context, id int64) (Run, error) {
	return single(s.runs(ctx, "WHERE r.id = $1", "", id))
}

func (s *Store) runs(ctx context.Context, where, limit string, args ...any) ([]Run, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.label, r.status, r.created_at, r.updated_at, r.finished_at,
			count(i.id),
			count(i.id) FILTER (WHERE i.status = 'pending'),
			count(i.id) FILTER (WHERE i.status = 'done'),
			count(i.id) FILTER (WHERE i.status = 'failed')
		FROM runs r
		LEFT JOIN run_items i ON i.run_id = r.id
		`+where+`
		GROUP BY r.id
		ORDER BY r.created_at DESC, r.id DESC
		`+limit, args...)
	if err != nil {
		return nil, fmt.Errorf("querying runs: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Run, error) {
		var r Run
		err := row.Scan(&r.ID, &r.Label, &r.Status, &r.CreatedAt, &r.UpdatedAt, &r.FinishedAt,
			&r.Total, &r.Pending, &r.Done, &r.Failed)
		return r, err
	})
}

// RunItems lists a run's items, failures first so they're easy to find.
func (s *Store) RunItems(ctx context.Context, runID int64) ([]RunItem, error) {
	if err := s.mustExist(ctx, "runs", runID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT i.id, i.kind, i.repo_url, i.path, i.status, i.error, i.scan_id, i.applied, i.updated_at,
			sc.project_id, sc.module_id
		FROM run_items i
		LEFT JOIN scans sc ON sc.id = i.scan_id
		WHERE i.run_id = $1
		ORDER BY CASE i.status WHEN 'failed' THEN 0 WHEN 'pending' THEN 1 ELSE 2 END,
			i.kind DESC, i.repo_url, i.path`, runID)
	if err != nil {
		return nil, fmt.Errorf("querying run items: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (RunItem, error) {
		var it RunItem
		err := row.Scan(&it.ID, &it.Kind, &it.RepoURL, &it.Path, &it.Status, &it.Error, &it.ScanID, &it.Applied,
			&it.UpdatedAt, &it.ProjectID, &it.ModuleID)
		return it, err
	})
}

func lockItem(ctx context.Context, tx pgx.Tx, runID, itemID int64) (RunItem, string, error) {
	var it RunItem
	var runStatus string
	err := tx.QueryRow(ctx, `
		SELECT i.id, i.kind, i.repo_url, i.path, i.status, i.scan_id, i.applied, r.status
		FROM run_items i JOIN runs r ON r.id = i.run_id
		WHERE i.id = $2 AND i.run_id = $1
		FOR UPDATE OF i`, runID, itemID,
	).Scan(&it.ID, &it.Kind, &it.RepoURL, &it.Path, &it.Status, &it.ScanID, &it.Applied, &runStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return RunItem{}, "", ErrNotFound
	}
	if err != nil {
		return RunItem{}, "", fmt.Errorf("locking run item: %w", err)
	}
	return it, runStatus, nil
}

func matchItem(it RunItem, r report.Report) error {
	wantType := map[string]string{
		ItemKindProject:    report.ScannerTypeModuleUsage,
		ItemKindModuleRepo: report.ScannerTypeModuleRepo,
	}[it.Kind]
	switch {
	case r.ScannerType != wantType:
		return fmt.Errorf("%w: item is a %s but the report is %s", ErrItemMismatch, it.Kind, r.ScannerType)
	case source.RepoKey(r.Subject.RepoURL) != source.RepoKey(it.RepoURL):
		return fmt.Errorf("%w: item is %s but the report is for %s", ErrItemMismatch, it.RepoURL, r.Subject.RepoURL)
	case it.Kind == ItemKindProject && r.Subject.ProjectPath() != it.Path:
		return fmt.Errorf("%w: item path is %s but the report's is %s", ErrItemMismatch, it.Path, r.Subject.ProjectPath())
	}
	return nil
}

func touchRun(ctx context.Context, tx pgx.Tx, runID int64) error {
	if _, err := tx.Exec(ctx, `UPDATE runs SET updated_at = now() WHERE id = $1`, runID); err != nil {
		return fmt.Errorf("updating run: %w", err)
	}
	return nil
}
