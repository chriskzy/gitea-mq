package batch_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Mic92/gitea-mq/internal/batch"
	"github.com/Mic92/gitea-mq/internal/forge"
	"github.com/Mic92/gitea-mq/internal/store/pg"
	"github.com/jackc/pgx/v5/pgtype"
)

func advisoryFixture(n int) ([]pg.QueueEntry, []forge.BatchInput) {
	entries, inputs := make([]pg.QueueEntry, n), make([]forge.BatchInput, n)
	for i := range entries {
		head := fmt.Sprintf("sha%d", i)
		entries[i] = pg.QueueEntry{PrHeadSha: head, EnqueuedAt: pgtype.Timestamptz{Time: time.Now(), Valid: true}}
		inputs[i] = forge.BatchInput{HeadSHA: head, Files: []string{fmt.Sprintf("app/file%d", i)}, Suites: []string{"contract", "app"}}
	}
	return entries, inputs
}

func TestAdaptiveSizesAndHardCap(t *testing.T) {
	for size := 1; size <= 15; size++ {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			entries, inputs := advisoryFixture(size)
			got, _ := batch.SelectPrefix(entries, inputs, time.Now())
			if got != min(size, 10) {
				t.Fatalf("size %d: got %d", size, got)
			}
		})
	}
	if n, _ := batch.SelectPrefix(nil, nil, time.Now()); n != 0 {
		t.Fatal(n)
	}
}

func TestAdaptiveBoundariesAndMissingData(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func([]pg.QueueEntry, []forge.BatchInput)
		want   int
	}{
		{"same_file", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[1].Files = p[0].Files }, 1},
		{"authority", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[1].Isolate = true }, 1},
		{"first_safety", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[0].Isolate = true }, 1},
		{"dependency", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[1].Blocked = true }, 1},
		{"first_dependency", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[0].Blocked = true }, 0},
		{"head_changed", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[1].HeadSHA = "changed" }, 1},
		{"unknown_files", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[1].Files = nil }, 1},
		{"unknown_suites", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[1].Suites = nil }, 1},
		{"mixed_missing_durations", func(_ []pg.QueueEntry, p []forge.BatchInput) { p[1].Suites = []string{"contract", "other"} }, 1},
		{"measured_marginal_saving", func(_ []pg.QueueEntry, p []forge.BatchInput) {
			p[0].Seconds = map[string]float64{"contract": 30, "app": 10}
			p[1].Suites = []string{"contract", "other"}
			p[1].Seconds = map[string]float64{"contract": 30, "other": 5}
		}, 3},
		{"measured_marginal_cost", func(_ []pg.QueueEntry, p []forge.BatchInput) {
			p[0].Seconds = map[string]float64{"contract": 30, "app": 10}
			p[1].Suites = []string{"contract", "other"}
			p[1].Seconds = map[string]float64{"contract": 30, "other": 50}
		}, 1},
		{"aged_head_no_extra_work", func(e []pg.QueueEntry, p []forge.BatchInput) {
			e[0].EnqueuedAt.Time = time.Now().Add(-2 * time.Hour)
			p[0].Seconds = map[string]float64{"contract": 30, "app": 10}
			p[1].Suites = []string{"contract", "other"}
			p[1].Seconds = map[string]float64{"contract": 30, "other": 5}
		}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entries, inputs := advisoryFixture(3)
			tc.change(entries, inputs)
			if got, reason := batch.SelectPrefix(entries, inputs, time.Now()); got != tc.want {
				t.Fatalf("got %d (%s), want %d", got, reason, tc.want)
			}
		})
	}
	entries, _ := advisoryFixture(3)
	if got, _ := batch.SelectPrefix(entries, nil, time.Now()); got != 1 {
		t.Fatal(got)
	}
}

type advisedForge struct {
	*forge.MockForge
	inputs func([]forge.BatchCandidate) []forge.BatchInput
}

func (f advisedForge) BatchInputs(_ context.Context, _, _, _, _ string, candidates []forge.BatchCandidate) ([]forge.BatchInput, error) {
	return f.inputs(candidates), nil
}

func TestAdaptiveFormationRaceAndFairness(t *testing.T) {
	e, f, svc, ctx := setup(t, 1, 2, 3)
	e.BatchMax, e.BatchPolicyPath = 10, "policy.json"
	e.Forge = advisedForge{f.MockForge, func(c []forge.BatchCandidate) []forge.BatchInput {
		_, inputs := advisoryFixture(len(c))
		for i := range c {
			inputs[i].HeadSHA = c[i].HeadSHA
		}
		inputs[1].Files = inputs[0].Files
		return inputs
	}}
	b, err := e.FormAndBuild(ctx, "main")
	if err != nil || b == nil || len(b.MemberIds) != 1 {
		t.Fatalf("batch=%+v err=%v", b, err)
	}
	first, _ := svc.GetEntriesByIDs(ctx, b.MemberIds)
	if first[0].PrNumber != 1 {
		t.Fatal("FIFO leader skipped")
	}
	f.merged[1] = true
	if err := e.HandlePass(ctx, b); err != nil {
		t.Fatal(err)
	}
	b, err = e.FormAndBuild(ctx, "main")
	if err != nil || b == nil {
		t.Fatal(err)
	}
	first, _ = svc.GetEntriesByIDs(ctx, b.MemberIds)
	if first[0].PrNumber != 2 {
		t.Fatal("incompatible head starved")
	}
}

func TestAdaptiveTenBisectsAndRecovers(t *testing.T) {
	e, f, _, ctx := setup(t, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11)
	e.BatchMax, e.BatchPolicyPath = 10, "policy.json"
	e.Forge = advisedForge{f.MockForge, func(c []forge.BatchCandidate) []forge.BatchInput {
		_, inputs := advisoryFixture(len(c))
		for i := range c {
			inputs[i].HeadSHA = c[i].HeadSHA
		}
		return inputs
	}}
	b, err := e.FormAndBuild(ctx, "main")
	if err != nil || b == nil || len(b.MemberIds) != 10 {
		t.Fatalf("batch=%+v err=%v", b, err)
	}
	if err := e.HandleFail(ctx, b, "native-test", ""); err != nil {
		t.Fatal(err)
	}
	if len(b.CurrentIds) != 5 {
		t.Fatal("first bisection", b.CurrentIds)
	}
	for n := int64(1); n <= 10; n++ {
		f.merged[n] = true
	}
	if err := e.ReconcileLive(ctx); err != nil {
		t.Fatal(err)
	}
	for b.State != pg.BatchStateDone {
		if err := e.HandlePass(ctx, b); err != nil {
			t.Fatal(err)
		}
	}
	if len(b.LandedIds) != 10 || len(b.EjectedIds) != 0 {
		t.Fatalf("lost members: %+v", b)
	}
	next, err := e.FormAndBuild(ctx, "main")
	if err != nil || next == nil || len(next.MemberIds) != 1 {
		t.Fatalf("lone ready PR stalled: %+v %v", next, err)
	}
}
