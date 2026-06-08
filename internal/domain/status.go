// Package domain defines the on-the-wire entities exchanged between
// clawtopd (writer) and clawtop (reader). The format is intentionally small
// and forward-compatible: unknown fields are ignored and missing fields fall
// back to zero values.
package domain

import "time"

// Schema version. Bump when making a breaking change.
const Version = 5

// Window represents a rate-limit window (e.g. 5h session or 7d weekly).
type Window struct {
	Pct     float64 `json:"pct"`
	ResetAt int64   `json:"reset_at"`
}

// BranchStat captures token usage for a (project, git-branch) pair.
type BranchStat struct {
	Branch   string `json:"branch"`
	In       int64  `json:"in"`
	Out      int64  `json:"out"`
	Sessions int    `json:"sessions"`
}

func (b BranchStat) Total() int64 { return b.In + b.Out }

// Project aggregates token usage attributed to a working directory.
type Project struct {
	Name     string       `json:"name"`        // basename of Path
	Path     string       `json:"path"`        // canonical absolute path (from transcript cwd)
	In       int64        `json:"in"`
	Out      int64        `json:"out"`
	CacheR   int64        `json:"cache_read"`
	CacheC   int64        `json:"cache_create"`
	Sessions int          `json:"sessions"`
	LastAt   int64        `json:"last_at"`
	PrevIn   int64        `json:"prev_in"`     // tokens in the period BEFORE the current window (same duration)
	PrevOut  int64        `json:"prev_out"`
	Branches []BranchStat `json:"branches,omitempty"` // git branches, sorted by tokens desc
}

func (p Project) PrevTotal() int64 { return p.PrevIn + p.PrevOut }

// Model aggregates token usage by model id (e.g. claude-opus-4-7).
type Model struct {
	Model      string `json:"model"`
	In         int64  `json:"in"`
	Out        int64  `json:"out"`
	CacheR     int64  `json:"cache_read"`
	CacheC     int64  `json:"cache_create"`
	Sessions   int    `json:"sessions"`
	PrevIn     int64  `json:"prev_in"`
	PrevOut    int64  `json:"prev_out"`
	WebSearch  int64  `json:"web_search"`  // server_tool_use.web_search_requests
	WebFetch   int64  `json:"web_fetch"`   // server_tool_use.web_fetch_requests
}

func (m Model) PrevTotal() int64 { return m.PrevIn + m.PrevOut }

// Status is the full payload emitted per machine per poll.
type Status struct {
	Schema       int           `json:"schema"`
	Machine      string        `json:"machine"`
	TS           int64         `json:"ts"`
	Session      Window        `json:"session"`
	Week         Window        `json:"week"`
	Limit        string        `json:"limit"`
	Subscription string        `json:"subscription"`
	Window       string        `json:"window"`
	Sessions     int           `json:"sessions"`
	ByProject    []Project     `json:"by_project"`
	ByModel      []Model       `json:"by_model"`
	Hourly24h    []int64       `json:"hourly_24h"`
	Daily7d      []int64       `json:"daily_7d"`
	TopSessions  []SessionStat `json:"top_sessions"`
	// Heatmap is a flat 168-int array representing 7 days × 24 hours of
	// tokens consumed, with day 0 = Monday, hour 0 = midnight. Index =
	// day*24 + hour. Flat instead of nested for compact JSON.
	Heatmap [168]int64 `json:"heatmap"`
	// Web tool counters at account level (also broken down per-model).
	WebSearch int64 `json:"web_search"`
	WebFetch  int64 `json:"web_fetch"`
	// Tool-action counters at account level over the window: how much real
	// work happened (edits/reads/shell commands), independent of token spend.
	Edits int64 `json:"edits"`
	Reads int64 `json:"reads"`
	Bash  int64 `json:"bash"`
}

// SessionStat summarizes a single Claude conversation worth of tokens.
type SessionStat struct {
	ID        string `json:"id"`
	Project   string `json:"project"`
	Model     string `json:"model"`
	In        int64  `json:"in"`
	Out       int64  `json:"out"`
	CacheR    int64  `json:"cache_read"`
	CacheC    int64  `json:"cache_create"`
	StartedAt int64  `json:"started_at"`
	LastAt    int64  `json:"last_at"`
	// Title is the human-readable session title (Claude Code's ai-title,
	// falling back to a truncated last prompt). Empty when neither is present.
	Title string `json:"title,omitempty"`
	// Tool-action counters: what the session actually did, not just its tokens.
	Edits        int `json:"edits"`         // Edit/Write/MultiEdit/NotebookEdit calls
	Reads        int `json:"reads"`         // Read/Glob/Grep calls
	Bash         int `json:"bash"`          // Bash calls
	FilesTouched int `json:"files_touched"` // distinct file paths edited/written
}

func (s SessionStat) Total() int64 { return s.In + s.Out }

// CacheHitRate returns the fraction of input tokens served from cache for this
// session, in [0, 100]. Zero when no input tokens at all.
func (s SessionStat) CacheHitRate() float64 {
	denom := s.In + s.CacheR
	if denom == 0 {
		return 0
	}
	return float64(s.CacheR) / float64(denom) * 100
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

// CacheHitRate returns the fraction of input tokens served from cache for
// this model, as a value in [0, 100]. Zero when no input tokens at all.
func (m Model) CacheHitRate() float64 {
	denom := m.In + m.CacheR
	if denom == 0 {
		return 0
	}
	return float64(m.CacheR) / float64(denom) * 100
}

// TrendPct returns the percentage change from previous to current period.
// Positive = grew, negative = shrunk. Returns 0 when previous was zero
// (no meaningful comparison).
func TrendPct(curr, prev int64) float64 {
	if prev == 0 {
		return 0
	}
	return (float64(curr) - float64(prev)) / float64(prev) * 100
}
