package scan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/WasathTheekshana/terragraph/scanner/internal/client"
	"github.com/WasathTheekshana/terragraph/scanner/internal/report"
)

// fakeServer implements the run API in memory.
type fakeServer struct {
	mu        sync.Mutex
	items     []client.Item
	submitted map[int64]report.ScanReport
	failed    map[int64]string
	finished  string
	// block, if set, holds submissions until closed.
	block chan struct{}
}

func (f *fakeServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch {
	case r.URL.Path == "/api/v1/runs":
		var req struct {
			Items []client.ItemSpec `json:"items"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		for i, s := range req.Items {
			f.items = append(f.items, client.Item{ID: int64(i + 1), ItemSpec: s})
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"run": map[string]any{"id": 42}, "items": f.items})
	case strings.HasSuffix(r.URL.Path, "/scan"):
		var id int64
		fmt.Sscanf(r.URL.Path, "/api/v1/runs/42/items/%d/scan", &id)
		var rep report.ScanReport
		_ = json.NewDecoder(r.Body).Decode(&rep)
		if f.block != nil {
			f.mu.Unlock()
			<-f.block
			f.mu.Lock()
		}
		f.submitted[id] = rep
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"scan_id": id, "applied": rep.Subject.Branch == "main"})
	case strings.HasSuffix(r.URL.Path, "/fail"):
		var id int64
		fmt.Sscanf(r.URL.Path, "/api/v1/runs/42/items/%d/fail", &id)
		var req struct{ Error string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.failed[id] = req.Error
		w.WriteHeader(http.StatusNoContent)
	case strings.HasSuffix(r.URL.Path, "/finish"):
		var req struct{ Status string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		f.finished = req.Status
		w.WriteHeader(http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

func newRunner(t *testing.T, f *fakeServer) (*Runner, *bytes.Buffer) {
	t.Helper()
	f.submitted, f.failed = map[int64]report.ScanReport{}, map[int64]string{}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	c := client.New(srv.URL, "tok")
	c.BaseDelay = time.Millisecond
	var out bytes.Buffer
	return &Runner{Client: c, Concurrency: 3, Out: &syncWriter{w: &out}, UIBase: "http://ui"}, &out
}

type syncWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

func projectTarget(repo, path, branch string) Target {
	r := report.New(report.ScannerTypeModuleUsage, report.Subject{Kind: report.SubjectKindProject, RepoURL: repo, Path: path, Branch: branch},
		[]report.Fact{{Type: report.FactTypeModuleCall, CallName: "vpc", Source: "x"}})
	return Target{Kind: client.ItemKindProject, RepoURL: repo, Path: path, Report: &r}
}

func TestRunnerReportsEveryItem(t *testing.T) {
	f := &fakeServer{}
	runner, out := newRunner(t, f)
	targets := []Target{
		projectTarget("git@github.com:org/infra.git", "envs/prod", "main"),
		projectTarget("git@github.com:org/infra.git", ".", "main"),
		projectTarget("https://github.com/org/api.git", ".", "develop"),
		{Kind: client.ItemKindProject, RepoURL: "file://laptop/scratch", Path: "broken", Err: errors.New("main.tf:1: unclosed block")},
	}

	sum, err := runner.Run(context.Background(), "test", targets)
	if err != nil {
		t.Fatal(err)
	}
	if sum.RunID != 42 || sum.Total != 4 || sum.Done != 3 || sum.Failed != 1 || sum.NotApplied != 1 {
		t.Errorf("summary = %+v", sum)
	}
	if len(f.submitted) != 3 || len(f.failed) != 1 || f.failed[4] != "main.tf:1: unclosed block" || f.finished != client.RunFinished {
		t.Errorf("server saw submitted=%d failed=%v finished=%q", len(f.submitted), f.failed, f.finished)
	}
	for _, want := range []string{"Scan #42", "http://ui/runs/42", "FAILED: main.tf:1: unclosed block", "recorded but not current", "(envs/prod)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRunnerCancels(t *testing.T) {
	f := &fakeServer{block: make(chan struct{})}
	runner, _ := newRunner(t, f)
	runner.Concurrency = 1
	targets := []Target{
		projectTarget("https://github.com/org/a.git", ".", "main"),
		projectTarget("https://github.com/org/b.git", ".", "main"),
		projectTarget("https://github.com/org/c.git", ".", "main"),
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	var sum Summary
	var err error
	go func() {
		sum, err = runner.Run(ctx, "test", targets)
		close(done)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	close(f.block)
	<-done

	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if f.finished != client.RunCancelled {
		t.Errorf("run closed as %q, want cancelled", f.finished)
	}
	if sum.Done+sum.Failed >= len(targets) {
		t.Errorf("summary = %+v; cancelling should stop before every item", sum)
	}
}
