package main

import (
	"math"
	"testing"

	"github.com/leonardorifeli/clawtop/internal/domain"
)

func TestPriceCost(t *testing.T) {
	// Opus list price: in $5, out $25, cache read $0.50, cache write $6.25 per 1M.
	p := defaultPricing()["opus"]
	// 1M in + 1M out + 1M cacheR + 1M cacheC = 5 + 25 + 0.5 + 6.25 = 36.75
	got := p.Cost(1_000_000, 1_000_000, 1_000_000, 1_000_000)
	if math.Abs(got-36.75) > 1e-9 {
		t.Fatalf("opus cost = %v, want 36.75", got)
	}
}

func TestModelCostByFamily(t *testing.T) {
	tbl := defaultPricing()
	cases := []struct {
		model string
		want  float64 // cost of exactly 1M output tokens
	}{
		{"claude-opus-4-8", 25},
		{"claude-sonnet-4-6", 15},
		{"claude-haiku-4-5", 5},
	}
	for _, c := range cases {
		got, ok := tbl.modelCost(domain.Model{Model: c.model, Out: 1_000_000})
		if !ok {
			t.Fatalf("%s: no price matched", c.model)
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: cost = %v, want %v", c.model, got, c.want)
		}
	}
	if _, ok := tbl.modelCost(domain.Model{Model: "some-unknown-model"}); ok {
		t.Error("unknown model should not match a price family")
	}
}

func TestLoadPricingOverride(t *testing.T) {
	// Missing path falls back to defaults without error.
	tbl := loadPricing("/nonexistent/pricing.json")
	if tbl["opus"].In != 5 {
		t.Fatalf("default opus.In = %v, want 5", tbl["opus"].In)
	}
}
