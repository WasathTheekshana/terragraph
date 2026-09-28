package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/WasathTheekshana/terragraph/server/internal/report"
	"github.com/WasathTheekshana/terragraph/server/internal/source"
	"github.com/WasathTheekshana/terragraph/server/internal/version"
)

// Scan is a validated report to ingest.
type Scan struct {
	Report report.Report
	// Raw is the report as submitted, kept for audit. Defaults to Report
	// re-encoded when empty.
	Raw json.RawMessage
	// Tracked reports whether a project scan comes from a branch whose state
	// should be shown as current. Module repo scans ignore it.
	Tracked bool
}

type IngestResult struct {
	ScanID int64 `json:"scan_id"`
	// Applied is false when the scan was only recorded in history: it came
	// from an untracked branch or is older than the current state.
	Applied bool `json:"applied"`
	// Duplicate is true when a run item had already been submitted; the
	// result is the original submission's.
	Duplicate bool `json:"duplicate,omitempty"`
}

func (s *Store) Ingest(ctx context.Context, in Scan) (IngestResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return IngestResult{}, err
	}
	defer tx.Rollback(ctx)

	res, err := ingest(ctx, tx, in)
	if err != nil {
		return IngestResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return IngestResult{}, err
	}
	return res, nil
}

func ingest(ctx context.Context, tx pgx.Tx, in Scan) (IngestResult, error) {
	if len(in.Raw) == 0 {
		raw, err := json.Marshal(in.Report)
		if err != nil {
			return IngestResult{}, err
		}
		in.Raw = raw
	}
	switch in.Report.ScannerType {
	case report.ScannerTypeModuleUsage:
		return ingestModuleUsage(ctx, tx, in)
	case report.ScannerTypeModuleRepo:
		return ingestModuleRepo(ctx, tx, in)
	default:
		return IngestResult{}, fmt.Errorf("unsupported scanner type %q", in.Report.ScannerType)
	}
}

func ingestModuleUsage(ctx context.Context, tx pgx.Tx, in Scan) (IngestResult, error) {
	r := in.Report

	repoKey := source.RepoKey(r.Subject.RepoURL)
	var repoID int64
	err := tx.QueryRow(ctx, `
		INSERT INTO repos (repo_key, repo_url) VALUES ($1, $2)
		ON CONFLICT (repo_key) DO UPDATE SET repo_url = EXCLUDED.repo_url
		RETURNING id`,
		repoKey, r.Subject.RepoURL,
	).Scan(&repoID)
	if err != nil {
		return IngestResult{}, fmt.Errorf("upserting repo: %w", err)
	}

	// The upsert row-locks the project, serializing concurrent scans of it.
	var projectID int64
	var lastScanAt *time.Time
	err = tx.QueryRow(ctx, `
		INSERT INTO projects (repo_id, repo_key, path, repo_url) VALUES ($1, $2, $3, $4)
		ON CONFLICT (repo_key, path) DO UPDATE SET repo_url = EXCLUDED.repo_url
		RETURNING id, last_scan_at`,
		repoID, repoKey, r.Subject.ProjectPath(), r.Subject.RepoURL,
	).Scan(&projectID, &lastScanAt)
	if err != nil {
		return IngestResult{}, fmt.Errorf("upserting project: %w", err)
	}

	applied := in.Tracked && (lastScanAt == nil || !r.GeneratedAt.Before(*lastScanAt))
	scanID, err := insertScan(ctx, tx, in, &projectID, nil, applied)
	if err != nil {
		return IngestResult{}, err
	}
	if !applied {
		return IngestResult{ScanID: scanID}, nil
	}

	moduleIDs, err := upsertModules(ctx, tx, r.Facts)
	if err != nil {
		return IngestResult{}, err
	}

	if _, err := tx.Exec(ctx, `DELETE FROM module_usages WHERE project_id = $1`, projectID); err != nil {
		return IngestResult{}, fmt.Errorf("clearing module usages: %w", err)
	}

	rows := make([][]any, 0, len(r.Facts))
	for _, f := range r.Facts {
		var moduleID any
		if kind, key := source.Parse(f.Source); kind != source.KindLocal {
			moduleID = moduleIDs[key]
		}
		major, minor, patch, pre := versionColumns(pinnedVersion(f))
		rows = append(rows, []any{
			projectID, f.Parent, f.CallName, moduleID, f.Source, f.RefDeclared, f.RefResolved, f.VersionResolved,
			f.ResolutionSource, f.File, f.Line, major, minor, patch, pre, scanID,
		})
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"module_usages"}, []string{
		"project_id", "parent", "call_name", "module_id", "source", "ref_declared", "ref_resolved", "version_resolved",
		"resolution_source", "file", "line", "major", "minor", "patch", "prerelease", "scan_id",
	}, pgx.CopyFromRows(rows))
	if err != nil {
		return IngestResult{}, fmt.Errorf("inserting module usages: %w", err)
	}

	_, err = tx.Exec(ctx, `
		UPDATE projects SET last_scan_at = $2, last_commit_sha = $3, last_branch = $4
		WHERE id = $1`,
		projectID, r.GeneratedAt, r.Subject.CommitSHA, r.Subject.Branch)
	if err != nil {
		return IngestResult{}, fmt.Errorf("updating project: %w", err)
	}
	return IngestResult{ScanID: scanID, Applied: true}, nil
}

func ingestModuleRepo(ctx context.Context, tx pgx.Tx, in Scan) (IngestResult, error) {
	r := in.Report

	var moduleID int64
	var scannedAt *time.Time
	err := tx.QueryRow(ctx, `
		INSERT INTO modules (source_key, kind, source) VALUES ($1, 'git', $2)
		ON CONFLICT (source_key) DO UPDATE SET kind = EXCLUDED.kind, source = EXCLUDED.source
		RETURNING id, versions_scanned_at`,
		source.RepoKey(r.Subject.RepoURL), r.Subject.RepoURL,
	).Scan(&moduleID, &scannedAt)
	if err != nil {
		return IngestResult{}, fmt.Errorf("upserting module: %w", err)
	}

	applied := scannedAt == nil || !r.GeneratedAt.Before(*scannedAt)
	scanID, err := insertScan(ctx, tx, in, nil, &moduleID, applied)
	if err != nil {
		return IngestResult{}, err
	}
	if !applied {
		return IngestResult{ScanID: scanID}, nil
	}

	// Replace rather than merge, so tags deleted upstream disappear.
	if _, err := tx.Exec(ctx, `DELETE FROM module_versions WHERE module_id = $1`, moduleID); err != nil {
		return IngestResult{}, fmt.Errorf("clearing module versions: %w", err)
	}
	rows := make([][]any, 0, len(r.Facts))
	for _, f := range r.Facts {
		v, ok := version.Parse(f.Tag)
		major, minor, patch, pre := versionColumns(v, ok)
		rows = append(rows, []any{moduleID, f.Tag, f.CommitSHA, major, minor, patch, pre})
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{"module_versions"},
		[]string{"module_id", "tag", "commit_sha", "major", "minor", "patch", "prerelease"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return IngestResult{}, fmt.Errorf("inserting module versions: %w", err)
	}

	if _, err := tx.Exec(ctx, `UPDATE modules SET versions_scanned_at = $2 WHERE id = $1`, moduleID, r.GeneratedAt); err != nil {
		return IngestResult{}, fmt.Errorf("updating module: %w", err)
	}
	return IngestResult{ScanID: scanID, Applied: true}, nil
}

func insertScan(ctx context.Context, tx pgx.Tx, in Scan, projectID, moduleID *int64, applied bool) (int64, error) {
	r := in.Report
	var id int64
	err := tx.QueryRow(ctx, `
		INSERT INTO scans (scanner_type, project_id, module_id, commit_sha, branch, generated_at, applied, report)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING id`,
		r.ScannerType, projectID, moduleID, r.Subject.CommitSHA, r.Subject.Branch, r.GeneratedAt, applied, in.Raw,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("inserting scan: %w", err)
	}
	return id, nil
}

// upsertModules makes sure every non-local module referenced by facts
// exists and returns their ids by source key.
func upsertModules(ctx context.Context, tx pgx.Tx, facts []report.Fact) (map[string]int64, error) {
	firstSource := make(map[string]string)
	kinds := make(map[string]source.Kind)
	for _, f := range facts {
		kind, key := source.Parse(f.Source)
		if kind == source.KindLocal {
			continue
		}
		if _, ok := firstSource[key]; !ok {
			firstSource[key] = f.Source
			kinds[key] = kind
		}
	}

	keys := make([]string, 0, len(firstSource))
	for k := range firstSource {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	kindCol := make([]string, len(keys))
	sourceCol := make([]string, len(keys))
	for i, k := range keys {
		kindCol[i] = string(kinds[k])
		sourceCol[i] = firstSource[k]
	}

	// DO NOTHING avoids locking existing module rows, so concurrent project
	// scans that share modules don't block each other.
	_, err := tx.Exec(ctx, `
		INSERT INTO modules (source_key, kind, source)
		SELECT * FROM unnest($1::text[], $2::text[], $3::text[])
		ON CONFLICT (source_key) DO NOTHING`,
		keys, kindCol, sourceCol)
	if err != nil {
		return nil, fmt.Errorf("upserting modules: %w", err)
	}

	rows, err := tx.Query(ctx, `SELECT source_key, id FROM modules WHERE source_key = ANY($1)`, keys)
	if err != nil {
		return nil, fmt.Errorf("looking up modules: %w", err)
	}
	ids := make(map[string]int64, len(keys))
	var key string
	var id int64
	_, err = pgx.ForEachRow(rows, []any{&key, &id}, func() error {
		ids[key] = id
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("looking up modules: %w", err)
	}
	return ids, nil
}

// pinnedVersion is the exact version a module call is on: the one Terraform
// selected if known, otherwise the declared ref if it's an exact version.
func pinnedVersion(f report.Fact) (version.Version, bool) {
	if f.VersionResolved != "" {
		return version.Parse(f.VersionResolved)
	}
	return version.Parse(f.RefDeclared)
}

func versionColumns(v version.Version, ok bool) (major, minor, patch any, prerelease string) {
	if !ok {
		return nil, nil, nil, ""
	}
	return v.Major, v.Minor, v.Patch, v.Prerelease
}
