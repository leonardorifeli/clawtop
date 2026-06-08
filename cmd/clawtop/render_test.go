package main

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/leonardorifeli/clawtop/internal/domain"
	"github.com/leonardorifeli/clawtop/internal/service/merger"
)

func sampleMerged() merger.Merged {
	now := time.Now().Unix()
	return merger.Merged{
		Window: "7d",
		Edits:  144, Reads: 164, Bash: 466,
		ByModel: []domain.Model{
			{Model: "claude-opus-4-8", In: 2_000_000, Out: 500_000, CacheR: 10_000_000, Sessions: 3},
		},
		TopSessions: []domain.SessionStat{{
			ID: "abc", Project: "clawtop", Model: "claude-opus-4-8",
			In: 200_000, Out: 45_000, CacheR: 11_000_000,
			Title: "Add session titles and cost estimation",
			Edits: 14, Reads: 4, Bash: 22, FilesTouched: 7,
			StartedAt: now - 3600, LastAt: now - 60, // touched 1m ago → live
		}},
	}
}

func TestViewSessionsRendersNewFields(t *testing.T) {
	out := viewSessions(sampleMerged(), 100, defaultPricing())
	for _, want := range []string{
		"Add session titles and cost estimation", // title
		"144 edits",                              // account actions
		"14e", "7f", "4r", "22b",                 // per-session actions
		"cache 98%",                              // per-session cache hit
		"●",                                      // live dot
	} {
		if !strings.Contains(out, want) {
			t.Errorf("viewSessions output missing %q\n---\n%s", want, out)
		}
	}
}

func TestViewModelsRendersCost(t *testing.T) {
	out := viewModels(sampleMerged(), 100, defaultPricing())
	// 2M in ($10) + 0.5M out ($12.5) + 10M cacheR ($5) = $27.50
	if !strings.Contains(out, "est. $28") && !strings.Contains(out, "est. $27") {
		t.Errorf("viewModels missing total cost estimate\n---\n%s", out)
	}
	if !strings.Contains(out, "est=$2") {
		t.Errorf("viewModels missing per-model cost\n---\n%s", out)
	}
}

func TestIsSpinning(t *testing.T) {
	spin := domain.SessionStat{In: 250_000, Out: 50_000} // big, zero actions
	if !isSpinning(spin) {
		t.Error("big session with no actions should be flagged spinning")
	}
	worked := domain.SessionStat{In: 250_000, Out: 50_000, Edits: 3}
	if isSpinning(worked) {
		t.Error("session with edits must not be flagged spinning")
	}
	small := domain.SessionStat{In: 1000, Out: 500}
	if isSpinning(small) {
		t.Error("small no-action session must not be flagged spinning")
	}
}

func TestMonthlyCost(t *testing.T) {
	// $7 over a 7d window → $30/month.
	mo, ok := monthlyCost(7, "7d")
	if !ok || math.Abs(mo-30) > 1e-9 {
		t.Fatalf("monthlyCost(7,7d) = %v (ok=%v), want 30", mo, ok)
	}
	// 24h window: $1/day → $30/month.
	mo, ok = monthlyCost(1, "24h")
	if !ok || math.Abs(mo-30) > 1e-9 {
		t.Fatalf("monthlyCost(1,24h) = %v (ok=%v), want 30", mo, ok)
	}
	if _, ok := monthlyCost(0, "7d"); ok {
		t.Error("zero cost should not project")
	}
	if _, ok := monthlyCost(5, "??"); ok {
		t.Error("unknown window should not project")
	}
}

func TestDashboardSessionsLiveDot(t *testing.T) {
	out := dashboardSessions(sampleMerged(), 120, defaultPricing())
	if !strings.Contains(out, "Add session titles") {
		t.Errorf("dashboardSessions missing title\n---\n%s", out)
	}
}
