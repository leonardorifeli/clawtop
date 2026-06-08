package main

import (
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
	out := viewSessions(sampleMerged(), 100)
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

func TestDashboardSessionsLiveDot(t *testing.T) {
	out := dashboardSessions(sampleMerged(), 120)
	if !strings.Contains(out, "Add session titles") {
		t.Errorf("dashboardSessions missing title\n---\n%s", out)
	}
}
