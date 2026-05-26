// Package collector reads Claude Code transcript JSONL files from
// ~/.claude/projects/<encoded-path>/<session-uuid>.jsonl and aggregates
// per-project, per-model, and hourly token usage.
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
	ByProject []domain.Project
	ByModel   []domain.Model
	Hourly24h []int64
}

type entry struct {
	Type    string `json:"type"`
	CWD     string `json:"cwd"`
	Message struct {
		Model string `json:"model"`
		Usage struct {
			Input               int64 `json:"input_tokens"`
			Output              int64 `json:"output_tokens"`
			CacheCreationInput  int64 `json:"cache_creation_input_tokens"`
			CacheReadInput      int64 `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	SessionID string `json:"sessionId"`
	Timestamp string `json:"timestamp"`
}

// Collect walks Opts.Root, parses every .jsonl file, and aggregates usage
// within the lookback window. Returns empty slices (not nil) on missing data
// so the consumer can rely on len() semantics.
func Collect(opts Options) (*Result, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Window <= 0 {
		opts.Window = 7 * 24 * time.Hour
	}
	cutoff := opts.Now.Add(-opts.Window)
	dayAgo := opts.Now.Add(-24 * time.Hour)

	projects := map[string]*domain.Project{}
	models := map[string]*domain.Model{}
	hourly := make([]int64, 24)
	// Session -> canonical cwd (from first message that has it).
	sessionCWD := map[string]string{}

	err := filepath.WalkDir(opts.Root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip unreadable subtrees rather than aborting
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		// First pass on this file: scan for the cwd of each session.
		// We do a single pass and resolve cwd on the fly: if a usage entry
		// arrives before any cwd in the same file, we hold it until cwd is
		// known. In practice the first user message (with cwd) appears
		// before any assistant usage, so the held set stays empty.
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
			apply(projects, models, hourly, dayAgo, p, cwd)
		}
		// Drain held entries now that we've seen all cwd announcements in the file.
		for _, p := range held {
			cwd := sessionCWD[p.session]
			apply(projects, models, hourly, dayAgo, p, cwd)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	return &Result{
		ByProject: sortProjects(projects),
		ByModel:   sortModels(models),
		Hourly24h: hourly,
	}, nil
}

func apply(
	projects map[string]*domain.Project,
	models map[string]*domain.Model,
	hourly []int64,
	dayAgo time.Time,
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
	// Project bucket — key on full path, displayed as basename.
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

	// Model bucket.
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

	// Hourly bucket — last 24h, oldest first.
	if p.ts.After(dayAgo) {
		idx := 23 - int(time.Since(p.ts)/time.Hour)
		if idx < 0 {
			idx = 0
		}
		if idx > 23 {
			idx = 23
		}
		hourly[idx] += p.in + p.out
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
