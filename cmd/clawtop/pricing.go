package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/leonardorifeli/clawtop/internal/domain"
)

// Price holds per-million-token USD rates for one model family. Cache rates
// follow Anthropic's published multipliers: cache read ≈ 0.1× input, 5-minute
// cache write ≈ 1.25× input. These are LIST prices and only an estimate —
// actual cost depends on plan, discounts, and rate changes. Override via
// --pricing <file.json> and always validate against your billing source.
type Price struct {
	In         float64 `json:"in"`          // $/1M input tokens
	Out        float64 `json:"out"`         // $/1M output tokens
	CacheRead  float64 `json:"cache_read"`  // $/1M cached-read tokens
	CacheWrite float64 `json:"cache_write"` // $/1M cache-write (5m TTL) tokens
}

// Cost returns the estimated USD cost for the given token counts.
func (p Price) Cost(in, out, cacheR, cacheC int64) float64 {
	return float64(in)/1e6*p.In +
		float64(out)/1e6*p.Out +
		float64(cacheR)/1e6*p.CacheRead +
		float64(cacheC)/1e6*p.CacheWrite
}

// pricingTable maps a model family key ("opus"/"sonnet"/"haiku") to its rates.
type pricingTable map[string]Price

// defaultPricing returns the built-in list-price estimates (USD per 1M tokens),
// sourced from Anthropic's public pricing for the Claude 4.x family. Treat as
// an estimate; override with --pricing for plan-specific or updated rates.
func defaultPricing() pricingTable {
	return pricingTable{
		"opus":   {In: 5, Out: 25, CacheRead: 0.5, CacheWrite: 6.25},
		"sonnet": {In: 3, Out: 15, CacheRead: 0.3, CacheWrite: 3.75},
		"haiku":  {In: 1, Out: 5, CacheRead: 0.1, CacheWrite: 1.25},
	}
}

// loadPricing overlays an optional JSON override file on top of the defaults.
// The file is a partial map keyed by family, e.g. {"opus": {"in": 4.5, ...}}.
// A missing or unreadable path falls back to defaults silently (cost is an
// optional convenience, not load-bearing).
func loadPricing(path string) pricingTable {
	t := defaultPricing()
	if path == "" {
		return t
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return t
	}
	var override pricingTable
	if json.Unmarshal(b, &override) != nil {
		return t
	}
	for k, v := range override {
		t[strings.ToLower(k)] = v
	}
	return t
}

// priceFor returns the rates for a model id by matching the family substring.
// ok is false when no family matches, so callers can render "n/a" instead of $0.
func (t pricingTable) priceFor(model string) (Price, bool) {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		p, ok := t["opus"]
		return p, ok
	case strings.Contains(m, "sonnet"):
		p, ok := t["sonnet"]
		return p, ok
	case strings.Contains(m, "haiku"):
		p, ok := t["haiku"]
		return p, ok
	}
	return Price{}, false
}

// modelCost estimates the USD cost of one model's usage. ok is false when the
// model family is unknown to the table.
func (t pricingTable) modelCost(m domain.Model) (float64, bool) {
	p, ok := t.priceFor(m.Model)
	if !ok {
		return 0, false
	}
	return p.Cost(m.In, m.Out, m.CacheR, m.CacheC), true
}

// sessionCost estimates the USD cost of one session's usage. ok is false when
// the session's model family is unknown to the table.
func (t pricingTable) sessionCost(s domain.SessionStat) (float64, bool) {
	p, ok := t.priceFor(s.Model)
	if !ok {
		return 0, false
	}
	return p.Cost(s.In, s.Out, s.CacheR, s.CacheC), true
}

// totalCost sums the estimated cost across all models with a known family.
func (t pricingTable) totalCost(models []domain.Model) float64 {
	var sum float64
	for _, m := range models {
		if c, ok := t.modelCost(m); ok {
			sum += c
		}
	}
	return sum
}

// fmtUSD renders a dollar amount compactly: cents below $10, whole-ish dollars
// up to $1k, then k/M suffixes.
func fmtUSD(v float64) string {
	switch {
	case v >= 1_000_000:
		return fmt.Sprintf("$%.1fM", v/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("$%.1fk", v/1_000)
	case v >= 10:
		return fmt.Sprintf("$%.0f", v)
	default:
		return fmt.Sprintf("$%.2f", v)
	}
}
