// Package domain defines the on-the-wire entities exchanged between
// clawtopd (writer) and clawtop (reader). The format is intentionally small
// and forward-compatible: unknown fields are ignored and missing fields fall
// back to zero values.
package domain

import "time"

// Schema version. Bump when making a breaking change.
const Version = 2

// Window represents a rate-limit window (e.g. 5h session or 7d weekly).
type Window struct {
	Pct     float64 `json:"pct"`
	ResetAt int64   `json:"reset_at"`
}

// Project aggregates token usage attributed to a working directory.
type Project struct {
	Name   string `json:"name"` // basename of Path
	Path   string `json:"path"` // canonical absolute path (from transcript cwd)
	In     int64  `json:"in"`
	Out    int64  `json:"out"`
	CacheR int64  `json:"cache_read"`
	CacheC int64  `json:"cache_create"`
}

// Model aggregates token usage by model id (e.g. claude-opus-4-7).
type Model struct {
	Model  string `json:"model"`
	In     int64  `json:"in"`
	Out    int64  `json:"out"`
	CacheR int64  `json:"cache_read"`
	CacheC int64  `json:"cache_create"`
}

// Status is the full payload emitted per machine per poll.
type Status struct {
	Schema       int       `json:"schema"`
	Machine      string    `json:"machine"`
	TS           int64     `json:"ts"`
	Session      Window    `json:"session"`
	Week         Window    `json:"week"`
	Limit        string    `json:"limit"`
	Subscription string    `json:"subscription"`
	Window       string    `json:"window"`      // aggregation lookback ("7d", "30d")
	ByProject    []Project `json:"by_project"`  // sorted desc by In+Out
	ByModel      []Model   `json:"by_model"`    // sorted desc by In+Out
	Hourly24h    []int64   `json:"hourly_24h"`  // 24 buckets, oldest first, In+Out
}

func (s Status) Age() time.Duration {
	return time.Since(time.Unix(s.TS, 0))
}

func (w Window) ResetIn() time.Duration {
	if w.ResetAt == 0 {
		return 0
	}
	d := time.Until(time.Unix(w.ResetAt, 0))
	if d < 0 {
		return 0
	}
	return d
}

func (p Project) Total() int64 { return p.In + p.Out }
func (m Model) Total() int64   { return m.In + m.Out }
