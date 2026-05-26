// Package domain defines the on-the-wire entities exchanged between
// clawtopd (writer) and clawtop (reader). The format is intentionally small
// and forward-compatible: unknown fields are ignored and missing fields fall
// back to zero values.
package domain

import "time"

// Schema version. Bump when making a breaking change.
const Version = 3

// Window represents a rate-limit window (e.g. 5h session or 7d weekly).
type Window struct {
	Pct     float64 `json:"pct"`
	ResetAt int64   `json:"reset_at"`
}

// Project aggregates token usage attributed to a working directory.
type Project struct {
	Name     string `json:"name"`           // basename of Path
	Path     string `json:"path"`           // canonical absolute path (from transcript cwd)
	In       int64  `json:"in"`
	Out      int64  `json:"out"`
	CacheR   int64  `json:"cache_read"`
	CacheC   int64  `json:"cache_create"`
	Sessions int    `json:"sessions"`       // distinct conversation count in the window
}

// Model aggregates token usage by model id (e.g. claude-opus-4-7).
type Model struct {
	Model    string `json:"model"`
	In       int64  `json:"in"`
	Out      int64  `json:"out"`
	CacheR   int64  `json:"cache_read"`
	CacheC   int64  `json:"cache_create"`
	Sessions int    `json:"sessions"`
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
	Window       string    `json:"window"`        // aggregation lookback ("7d", "30d")
	Sessions     int       `json:"sessions"`      // distinct sessions seen on this machine
	ByProject    []Project `json:"by_project"`    // sorted desc by In+Out
	ByModel      []Model   `json:"by_model"`      // sorted desc by In+Out
	Hourly24h    []int64   `json:"hourly_24h"`    // 24 buckets, oldest first, In+Out
	Daily7d      []int64   `json:"daily_7d"`      // 7 buckets, oldest first, In+Out
	TopSessions  []SessionStat `json:"top_sessions"` // top N most expensive sessions
}

// SessionStat summarizes a single Claude conversation worth of tokens.
type SessionStat struct {
	ID        string `json:"id"`
	Project   string `json:"project"`    // basename of cwd
	Model     string `json:"model"`      // dominant model id
	In        int64  `json:"in"`
	Out       int64  `json:"out"`
	StartedAt int64  `json:"started_at"` // unix seconds of first usage event
	LastAt    int64  `json:"last_at"`    // unix seconds of most recent usage event
}

func (s SessionStat) Total() int64 { return s.In + s.Out }

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

// CacheHitRate returns the fraction of input tokens served from cache for
// this model, as a value in [0, 100]. Zero when no input tokens at all.
func (m Model) CacheHitRate() float64 {
	denom := m.In + m.CacheR
	if denom == 0 {
		return 0
	}
	return float64(m.CacheR) / float64(denom) * 100
}
