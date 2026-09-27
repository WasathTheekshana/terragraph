package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The expressions below expect module_usages aliased as u and
// module_latest_versions as lv.
const (
	pinnedVersionSQL = `CASE WHEN u.major IS NULL THEN NULL
		ELSE u.major || '.' || u.minor || '.' || u.patch
			|| CASE WHEN u.prerelease <> '' THEN '-' || u.prerelease ELSE '' END END`

	latestVersionSQL = `CASE WHEN lv.major IS NULL THEN NULL
		ELSE lv.major || '.' || lv.minor || '.' || lv.patch END`

	// A prerelease of the latest release's version precedes it.
	outdatedSQL = `(lv.major IS NOT NULL AND u.major IS NOT NULL AND (
		(u.major, u.minor, u.patch) < (lv.major, lv.minor, lv.patch)
		OR ((u.major, u.minor, u.patch) = (lv.major, lv.minor, lv.patch) AND u.prerelease <> '')))`

	majorsBehindSQL = `CASE WHEN lv.major IS NULL OR u.major IS NULL THEN NULL
		ELSE GREATEST(lv.major - u.major, 0) END`
)

type Project struct {
	ID      int64  `json:"id"`
	RepoURL string `json:"repo_url"`
	// Path is the Terraform root inside the repo; "." is the repo root.
	Path          string     `json:"path"`
	LastScanAt    *time.Time `json:"last_scan_at"`
	LastCommitSHA string     `json:"last_commit_sha"`
	LastBranch    string     `json:"last_branch"`
	ModuleCalls   int        `json:"module_calls"`
	// OutdatedCalls counts module calls pinned below the latest release;
	// MajorBehindCalls counts those at least one major version behind.
	OutdatedCalls    int `json:"outdated_calls"`
	MajorBehindCalls int `json:"major_behind_calls"`
}

type Module struct {
	ID                int64      `json:"id"`
	Key               string     `json:"key"`
	Kind              string     `json:"kind"`
	Source            string     `json:"source"`
	VersionsScannedAt *time.Time `json:"versions_scanned_at"`
	LatestTag         *string    `json:"latest_tag"`
	LatestVersion     *string    `json:"latest_version"`
	Consumers         int        `json:"consumers"`
	OutdatedConsumers int        `json:"outdated_consumers"`
}

// Usage is one module call in a project's current state.
type Usage struct {
	ProjectID        int64   `json:"project_id"`
	ProjectRepoURL   string  `json:"project_repo_url"`
	ProjectPath      string  `json:"project_path"`
	CallName         string  `json:"call_name"`
	ModuleID         *int64  `json:"module_id"`
	ModuleKey        *string `json:"module_key"`
	ModuleKind       *string `json:"module_kind"`
	Source           string  `json:"source"`
	RefDeclared      string  `json:"ref_declared"`
	RefResolved      string  `json:"ref_resolved"`
	VersionResolved  string  `json:"version_resolved"`
	ResolutionSource string  `json:"resolution_source"`
	File             string  `json:"file"`
	Line             int     `json:"line"`
	PinnedVersion    *string `json:"pinned_version"`
	LatestTag        *string `json:"latest_tag"`
	LatestVersion    *string `json:"latest_version"`
	MajorsBehind     *int    `json:"majors_behind"`
	Outdated         bool    `json:"outdated"`
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	return s.projects(ctx, "")
}

func (s *Store) GetProject(ctx context.Context, id int64) (Project, error) {
	return single(s.projects(ctx, "WHERE p.id = $1", id))
}

func (s *Store) projects(ctx context.Context, where string, args ...any) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.repo_url, p.path, p.last_scan_at, p.last_commit_sha, p.last_branch,
			count(u.call_name),
			count(*) FILTER (WHERE `+outdatedSQL+`),
			count(*) FILTER (WHERE lv.major > u.major)
		FROM projects p
		LEFT JOIN module_usages u ON u.project_id = p.id
		LEFT JOIN module_latest_versions lv ON lv.module_id = u.module_id
		`+where+`
		GROUP BY p.id
		ORDER BY p.repo_url, p.path`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying projects: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Project, error) {
		var p Project
		err := row.Scan(&p.ID, &p.RepoURL, &p.Path, &p.LastScanAt, &p.LastCommitSHA, &p.LastBranch,
			&p.ModuleCalls, &p.OutdatedCalls, &p.MajorBehindCalls)
		return p, err
	})
}

func (s *Store) ListModules(ctx context.Context) ([]Module, error) {
	return s.modules(ctx, "")
}

func (s *Store) GetModule(ctx context.Context, id int64) (Module, error) {
	return single(s.modules(ctx, "WHERE m.id = $1", id))
}

func (s *Store) modules(ctx context.Context, where string, args ...any) ([]Module, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, m.source_key, m.kind, m.source, m.versions_scanned_at,
			lv.tag, `+latestVersionSQL+`,
			count(DISTINCT u.project_id),
			count(DISTINCT u.project_id) FILTER (WHERE `+outdatedSQL+`)
		FROM modules m
		LEFT JOIN module_latest_versions lv ON lv.module_id = m.id
		LEFT JOIN module_usages u ON u.module_id = m.id
		`+where+`
		GROUP BY m.id, lv.tag, lv.major, lv.minor, lv.patch
		ORDER BY m.source_key`, args...)
	if err != nil {
		return nil, fmt.Errorf("querying modules: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Module, error) {
		var m Module
		err := row.Scan(&m.ID, &m.Key, &m.Kind, &m.Source, &m.VersionsScannedAt,
			&m.LatestTag, &m.LatestVersion, &m.Consumers, &m.OutdatedConsumers)
		return m, err
	})
}

func single[T any](rows []T, err error) (T, error) {
	var zero T
	if err != nil {
		return zero, err
	}
	if len(rows) == 0 {
		return zero, ErrNotFound
	}
	return rows[0], nil
}

// ProjectUsages returns the module calls in a project's current state.
func (s *Store) ProjectUsages(ctx context.Context, projectID int64) ([]Usage, error) {
	if err := s.mustExist(ctx, "projects", projectID); err != nil {
		return nil, err
	}
	return s.usages(ctx, `u.project_id = $1 ORDER BY u.file, u.line, u.call_name`, projectID)
}

// ModuleConsumers returns every module call, across all projects, that uses
// the module: the blast radius of changing it.
func (s *Store) ModuleConsumers(ctx context.Context, moduleID int64) ([]Usage, error) {
	if err := s.mustExist(ctx, "modules", moduleID); err != nil {
		return nil, err
	}
	return s.usages(ctx, `u.module_id = $1 ORDER BY p.repo_url, p.path, u.call_name`, moduleID)
}

func (s *Store) usages(ctx context.Context, where string, args ...any) ([]Usage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.repo_url, p.path, u.call_name, u.module_id, m.source_key, m.kind, u.source,
			u.ref_declared, u.ref_resolved, u.version_resolved, u.resolution_source, u.file, u.line,
			`+pinnedVersionSQL+`, lv.tag, `+latestVersionSQL+`, `+majorsBehindSQL+`, `+outdatedSQL+`
		FROM module_usages u
		JOIN projects p ON p.id = u.project_id
		LEFT JOIN modules m ON m.id = u.module_id
		LEFT JOIN module_latest_versions lv ON lv.module_id = u.module_id
		WHERE `+where, args...)
	if err != nil {
		return nil, fmt.Errorf("querying module usages: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Usage, error) {
		var u Usage
		err := row.Scan(&u.ProjectID, &u.ProjectRepoURL, &u.ProjectPath, &u.CallName, &u.ModuleID, &u.ModuleKey, &u.ModuleKind, &u.Source,
			&u.RefDeclared, &u.RefResolved, &u.VersionResolved, &u.ResolutionSource, &u.File, &u.Line,
			&u.PinnedVersion, &u.LatestTag, &u.LatestVersion, &u.MajorsBehind, &u.Outdated)
		return u, err
	})
}

// mustExist returns ErrNotFound unless table has a row with the id. table is
// always a constant from this package.
func (s *Store) mustExist(ctx context.Context, table string, id int64) error {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE id = $1)`, id).Scan(&exists)
	if err != nil {
		return fmt.Errorf("looking up %s %d: %w", table, id, err)
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}
