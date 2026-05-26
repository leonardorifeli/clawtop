// Package collector reads Claude Code transcript JSONL files from
// ~/.claude/projects/<encoded-path>/<session-uuid>.jsonl and aggregates
// per-project, per-model, hourly, and daily token usage, plus distinct
// session counts.
//
// Each session has an authoritative working directory recorded in user/
// attachment events as the "cwd" field; we use that as the canonical project
// path instead of decoding the directory name (which is ambiguous because
// both '/' and '.' are encoded as '-').
package collector

import (
	"bufio"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/leonardorifeli/clawtop/internal/domain"
)

type Options struct {
	Root   string        // ~/.claude/projects
	Window time.Duration // lookback window for aggregation
	Now    time.Time     // injectable clock; zero means time.Now()
}

type Result struct {
	ByProject   []domain.Project
	ByModel     []domain.Model
	Hourly24h   []int64
	Daily7d     []int64
	Sessions    int // distinct sessions on this machine in the window
	TopSessions []domain.SessionStat
}

type entry struct {
	Type    string `json:"type"`
	CWD     string `json:"cwd"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			Input              int64 `json:"input_tokens"`
			Output             int64 `json:"output_tokens"`
			CacheCreationInput int64 `json:"cache_creation_input_tokens"`
			CacheReadInput     int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
}

// Collect walks Opts.Root, parses every .jsonl file, and aggregates usage
// within the lookback window.
func Collect(opts Options) (*Result, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Window <= 0 {
		opts.Window = 7 * 24 * time.Hour
	}
	cutoff := opts.Now.Add(-opts.Window)
	dayAgo := opts.Now.Add(-24 * time.Hour)
	weekAgo := opts.Now.Add(-7 * 24 * time.Hour)

	projects := map[string]*domain.Project{}
	models := map[string]*domain.Model{}
	hourly := make([]int64, 24)
	daily := make([]int64, 7)
	sessionCWD := map[string]string{}
	projSessions := map[string]map[string]struct{}{}
	modelSessions := map[string]map[string]struct{}{}
	allSessions := map[string]struct{}{}
	// Per-session aggregates for the "top sessions" panel.
	sessAgg := map[string]*domain.SessionStat{}

	err := filepath.WalkDir(opts.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		type pending struct {
			session string
			model   string
			in, out int64
			cacheR  int64
			cacheC  int64
			ts      time.Time
		}
		var held []pending

		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 || line[0] != '{' {
				continue
			}
			var e entry
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}

			if e.CWD != "" && e.SessionID != "" {
				if _, ok := sessionCWD[e.SessionID]; !ok {
					sessionCWD[e.SessionID] = e.CWD
				}
			}

			if e.Type != "assistant" || e.Message.Usage.Input == 0 && e.Message.Usage.Output == 0 {
				continue
			}
			ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
			if err != nil || ts.Before(cutoff) {
				continue
			}

			p := pending{
				session: e.SessionID,
				model:   e.Message.Model,
				in:      e.Message.Usage.Input,
				out:     e.Message.Usage.Output,
				cacheR:  e.Message.Usage.CacheReadInput,
				cacheC:  e.Message.Usage.CacheCreationInput,
				ts:      ts,
			}

			cwd, ok := sessionCWD[p.session]
			if !ok {
				held = append(held, p)
				continue
			}
			apply(projects, models, hourly, daily, projSessions, modelSessions, allSessions, sessAgg,
				dayAgo, weekAgo, p, cwd)
		}
		for _, p := range held {
			cwd := sessionCWD[p.session]
			apply(projects, models, hourly, daily, projSessions, modelSessions, allSessions, sessAgg,
				dayAgo, weekAgo, p, cwd)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	for path, pj := range projects {
		pj.Sessions = len(projSessions[path])
	}
	for id, m := range models {
		m.Sessions = len(modelSessions[id])
	}

	return &Result{
		ByProject:   sortProjects(projects),
		ByModel:     sortModels(models),
		Hourly24h:   hourly,
		Daily7d:     daily,
		Sessions:    len(allSessions),
		TopSessions: topSessions(sessAgg, 10),
	}, nil
}

func apply(
	projects map[string]*domain.Project,
	models map[string]*domain.Model,
	hourly []int64,
	daily []int64,
	projSessions map[string]map[string]struct{},
	modelSessions map[string]map[string]struct{},
	allSessions map[string]struct{},
	sessAgg map[string]*domain.SessionStat,
	dayAgo, weekAgo time.Time,
	p struct {
		session string
		model   string
		in, out int64
		cacheR  int64
		cacheC  int64
		ts      time.Time
	},
	cwd string,
) {
	key := cwd
	if key == "" {
		key = "(unknown)"
	}
	pj, ok := projects[key]
	if !ok {
		pj = &domain.Project{Path: key, Name: basename(key)}
		projects[key] = pj
	}
	pj.In += p.in
	pj.Out += p.out
	pj.CacheR += p.cacheR
	pj.CacheC += p.cacheC

	if p.model != "" {
		m, ok := models[p.model]
		if !ok {
			m = &domain.Model{Model: p.model}
			models[p.model] = m
		}
		m.In += p.in
		m.Out += p.out
		m.CacheR += p.cacheR
		m.CacheC += p.cacheC
	}

	if p.session != "" {
		allSessions[p.session] = struct{}{}
		if projSessions[key] == nil {
			projSessions[key] = map[string]struct{}{}
		}
		projSessions[key][p.session] = struct{}{}
		if p.model != "" {
			if modelSessions[p.model] == nil {
				modelSessions[p.model] = map[string]struct{}{}
			}
			modelSessions[p.model][p.session] = struct{}{}
		}
		ts := p.ts.Unix()
		sa, ok := sessAgg[p.session]
		if !ok {
			sa = &domain.SessionStat{
				ID:        p.session,
				Project:   basename(key),
				Model:     p.model,
				StartedAt: ts,
				LastAt:    ts,
			}
			sessAgg[p.session] = sa
		}
		sa.In += p.in
		sa.Out += p.out
		if ts < sa.StartedAt {
			sa.StartedAt = ts
		}
		if ts > sa.LastAt {
			sa.LastAt = ts
		}
	}

	tokens := p.in + p.out
	if p.ts.After(dayAgo) {
		idx := 23 - int(time.Since(p.ts)/time.Hour)
		if idx < 0 {
			idx = 0
		}
		if idx > 23 {
			idx = 23
		}
		hourly[idx] += tokens
	}
	if p.ts.After(weekAgo) {
		// Daily bucket: 6 = most recent day (today), 0 = 6 days ago.
		idx := 6 - int(time.Since(p.ts)/(24*time.Hour))
		if idx < 0 {
			idx = 0
		}
		if idx > 6 {
			idx = 6
		}
		daily[idx] += tokens
	}
}

func basename(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.LastIndex(p, "/"); i >= 0 && i < len(p)-1 {
		return p[i+1:]
	}
	return p
}

func sortProjects(m map[string]*domain.Project) []domain.Project {
	out := make([]domain.Project, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total() > out[j].Total() })
	return out
}

func topSessions(m map[string]*domain.SessionStat, n int) []domain.SessionStat {
	out := make([]domain.SessionStat, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total() > out[j].Total() })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func sortModels(m map[string]*domain.Model) []domain.Model {
	out := make([]domain.Model, 0, len(m))
	for _, v := range m {
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total() > out[j].Total() })
	return out
}

// DefaultRoot returns the standard Claude Code transcripts root for the
// current user, honoring $CLAUDE_CONFIG_DIR if set.
func DefaultRoot() string {
	if v := os.Getenv("CLAUDE_CONFIG_DIR"); v != "" {
		return filepath.Join(v, "projects")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}
