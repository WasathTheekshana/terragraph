package scan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/WasathTheekshana/terragraph/scanner/internal/client"
)

type Summary struct {
	RunID      int64
	Total      int
	Done       int
	Failed     int
	NotApplied int
}

type Runner struct {
	Client      *client.Client
	Concurrency int
	// Out receives one progress line per finished item.
	Out io.Writer
	// UIBase is the server's web address, for linking to the run's page.
	UIBase string
}

type outcome struct {
	target  Target
	applied bool
	detail  string
	err     error
}

// Run creates a run for targets and reports each one, in parallel. A target
// that fails is recorded as failed and the rest carry on. If ctx is
// cancelled, in-flight items finish and the run is closed as cancelled.
func (r *Runner) Run(ctx context.Context, label string, targets []Target) (Summary, error) {
	specs := make([]client.ItemSpec, len(targets))
	for i, t := range targets {
		specs[i] = t.spec()
	}
	run, err := r.Client.CreateRun(ctx, label, specs)
	if err != nil {
		return Summary{}, err
	}
	ids := make(map[client.ItemSpec]int64, len(run.Items))
	for _, it := range run.Items {
		ids[it.ItemSpec] = it.ID
	}
	for _, t := range targets {
		if _, ok := ids[t.spec()]; !ok {
			return Summary{RunID: run.ID}, fmt.Errorf("server did not register item %s", t)
		}
	}
	fmt.Fprintf(r.Out, "Scan #%d: %d items. Follow it at %s/runs/%d\n", run.ID, len(targets), r.UIBase, run.ID)

	sum := Summary{RunID: run.ID, Total: len(targets)}
	var mu sync.Mutex
	report := func(o outcome) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case o.err != nil:
			sum.Failed++
		case !o.applied:
			sum.NotApplied++
			sum.Done++
		default:
			sum.Done++
		}
		r.print(sum.Done+sum.Failed, sum.Total, o)
	}

	work := make(chan Target)
	var wg sync.WaitGroup
	for range max(r.Concurrency, 1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range work {
				report(r.process(ctx, run.ID, ids[t.spec()], t))
			}
		}()
	}
	for _, t := range targets {
		if ctx.Err() != nil {
			break
		}
		work <- t
	}
	close(work)
	wg.Wait()

	// Closing the run must survive the scan being cancelled.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	status := client.RunFinished
	if ctx.Err() != nil {
		status = client.RunCancelled
	}
	if err := r.Client.FinishRun(finishCtx, run.ID, status); err != nil {
		return sum, fmt.Errorf("closing run: %w", err)
	}
	if ctx.Err() != nil {
		return sum, ctx.Err()
	}
	return sum, nil
}

func (r *Runner) process(ctx context.Context, runID, itemID int64, t Target) outcome {
	rep, err := build(ctx, t)
	if err != nil {
		r.fail(ctx, runID, itemID, err)
		return outcome{target: t, err: err}
	}
	res, err := r.Client.SubmitItem(ctx, runID, itemID, rep)
	if err != nil {
		err = fmt.Errorf("submitting: %w", err)
		r.fail(ctx, runID, itemID, err)
		return outcome{target: t, err: err}
	}
	detail := plural(len(rep.Facts), "module call", "module calls")
	if t.Kind == client.ItemKindModuleRepo {
		detail = plural(len(rep.Facts), "tag", "tags")
	}
	return outcome{target: t, applied: res.Applied, detail: detail}
}

// fail records a failed item. If the server can't be told either, the run
// page shows the item as still waiting, which is accurate.
func (r *Runner) fail(ctx context.Context, runID, itemID int64, err error) {
	if ctx.Err() != nil {
		return
	}
	_ = r.Client.FailItem(ctx, runID, itemID, err.Error())
}

func (r *Runner) print(n, total int, o outcome) {
	kind := "project"
	if o.target.Kind == client.ItemKindModuleRepo {
		kind = "modules"
	}
	width := len(fmt.Sprint(total))
	prefix := fmt.Sprintf("[%*d/%d] %-7s %s", width, n, total, kind, o.target)
	switch {
	case o.err != nil:
		if errors.Is(o.err, context.Canceled) {
			fmt.Fprintf(r.Out, "%s  cancelled\n", prefix)
			return
		}
		fmt.Fprintf(r.Out, "%s  FAILED: %v\n", prefix, o.err)
	case !o.applied:
		fmt.Fprintf(r.Out, "%s  %s, recorded but not current (untracked branch, or a newer scan exists)\n", prefix, o.detail)
	default:
		fmt.Fprintf(r.Out, "%s  %s\n", prefix, o.detail)
	}
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}
