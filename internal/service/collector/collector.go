// Package collector reads Claude Code transcript JSONL files from
// ~/.claude/projects/<encoded-path>/<session-uuid>.jsonl and aggregates
// per-project, per-model, hourly, daily, branch, and heatmap token usage,
// plus distinct session counts and account-level web-tool counters.
//
// Each session has an authoritative working directory recorded in user/
// attachment events as the "cwd" field; we use that as the canonical project
// path instead of decoding the directory name. gitBranch is recorded on the
// same events and used to attribute tokens to branches within a project.
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
	Sessions    int
	TopSessions []domain.SessionStat
	Heatmap     [168]int64
	WebSearch   int64
	WebFetch    int64
	Edits       int64
	Reads       int64
	Bash        int64
}

type entry struct {
	Type      string `json:"type"`
	CWD       string `json:"cwd"`
	GitBranch string `json:"gitBranch"`
	Message   struct {
		Model string `json:"model"`
		Usage struct {
			Input              int64 `json:"input_tokens"`
			Output             int64 `json:"output_tokens"`
			CacheCreationInput int64 `json:"cache_creation_input_tokens"`
			CacheReadInput     int64 `json:"cache_read_input_tokens"`
			ServerToolUse      struct {
				WebSearchRequests int64 `json:"web_search_requests"`
				WebFetchRequests  int64 `json:"web_fetch_requests"`
			} `json:"server_tool_use"`
		} `json:"usage"`
		// Content is left raw: assistant turns carry an array of blocks,
		// other roles carry a plain string. We decode it lazily only for
		// assistant entries, so a string value never breaks the outer parse.
		Content json.RawMessage `json:"content"`
	} `json:"message"`
	SessionID  string `json:"sessionId"`
	Timestamp  string `json:"timestamp"`
	AITitle    string `json:"aiTitle"`    // on "ai-title" entries
	LastPrompt string `json:"lastPrompt"` // on "last-prompt" entries
}

// contentBlock is a single block of an assistant message. We only care about
// tool_use blocks: their name tells us what action was taken and, for edit
// tools, input.file_path tells us which file.
type contentBlock struct {
	Type  string `json:"type"`
	Name  string `json:"name"`
	Input struct {
		FilePath string `json:"file_path"`
	} `json:"input"`
}

// toolKind classifies a tool name into an action bucket. Unknown tools (the
// many MCP/server tools) fall through to "" and are ignored.
func toolKind(name string) string {
	switch name {
	case "Edit", "Write", "MultiEdit", "NotebookEdit":
		return "edit"
	case "Read", "Glob", "Grep":
		return "read"
	case "Bash":
		return "bash"
	default:
		return ""
	}
}

// branchKey: project path + "\x00" + branch name. Null byte is safe since
// neither component contains it.
func branchKey(path, branch string) string { return path + "\x00" + branch }

// state holds all the in-progress aggregators for one Collect call.
type state struct {
	projects       map[string]*domain.Project
	models         map[string]*domain.Model
	hourly         []int64
	daily          []int64
	heatmap        [168]int64
	webSearch      int64
	webFetch       int64
	sessionCWD     map[string]string
	sessionBranch  map[string]string
	sessionTitle   map[string]string
	sessionPrompt  map[string]string
	sessionFiles   map[string]map[string]struct{} // distinct files edited per session
	projSessions   map[string]map[string]struct{}
	modelSessions  map[string]map[string]struct{}
	allSessions    map[string]struct{}
	sessAgg        map[string]*domain.SessionStat
	branches       map[string]*domain.BranchStat // key = branchKey(path, branch)
	branchSessions map[string]map[string]struct{}
	edits          int64
	reads          int64
	bash           int64
	cutoff         time.Time
	prevCutoff     time.Time // start of the previous comparison window
	dayAgo         time.Time
	weekAgo        time.Time
	now            time.Time
}

func newState(opts Options) *state {
	return &state{
		projects:       map[string]*domain.Project{},
		models:         map[string]*domain.Model{},
		hourly:         make([]int64, 24),
		daily:          make([]int64, 7),
		sessionCWD:     map[string]string{},
		sessionBranch:  map[string]string{},
		sessionTitle:   map[string]string{},
		sessionPrompt:  map[string]string{},
		sessionFiles:   map[string]map[string]struct{}{},
		projSessions:   map[string]map[string]struct{}{},
		modelSessions:  map[string]map[string]struct{}{},
		allSessions:    map[string]struct{}{},
		sessAgg:        map[string]*domain.SessionStat{},
		branches:       map[string]*domain.BranchStat{},
		branchSessions: map[string]map[string]struct{}{},
		cutoff:         opts.Now.Add(-opts.Window),
		prevCutoff:     opts.Now.Add(-2 * opts.Window),
		dayAgo:         opts.Now.Add(-24 * time.Hour),
		weekAgo:        opts.Now.Add(-7 * 24 * time.Hour),
		now:            opts.Now,
	}
}

// Collect walks Opts.Root, parses every .jsonl file, and aggregates usage
// within the lookback window plus the equal-sized period BEFORE it (for
// trend comparison).
func Collect(opts Options) (*Result, error) {
	if opts.Now.IsZero() {
		opts.Now = time.Now()
	}
	if opts.Window <= 0 {
		opts.Window = 7 * 24 * time.Hour
	}

	s := newState(opts)

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
		s.scanFile(f)
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Stamp session counts onto the per-project/per-model aggregates.
	for path, pj := range s.projects {
		pj.Sessions = len(s.projSessions[path])
	}
	for id, m := range s.models {
		m.Sessions = len(s.modelSessions[id])
	}
	for key, b := range s.branches {
		b.Sessions = len(s.branchSessions[key])
	}

	// Attach branch lists to projects (sorted by tokens desc, capped at 8).
	branchesByProject := map[string][]domain.BranchStat{}
	for key, b := range s.branches {
		path := key[:strings.IndexByte(key, '\x00')]
		branchesByProject[path] = append(branchesByProject[path], *b)
	}
	for path, list := range branchesByProject {
		sort.Slice(list, func(i, j int) bool { return list[i].Total() > list[j].Total() })
		if len(list) > 8 {
			list = list[:8]
		}
		if pj, ok := s.projects[path]; ok {
			pj.Branches = list
		}
	}

	// Stamp title (ai-title, else truncated last prompt) and distinct
	// files-touched count onto each session aggregate.
	for id, sa := range s.sessAgg {
		if t := s.sessionTitle[id]; t != "" {
			sa.Title = t
		} else if pr := s.sessionPrompt[id]; pr != "" {
			sa.Title = truncatePrompt(pr, 80)
		}
		sa.FilesTouched = len(s.sessionFiles[id])
	}

	return &Result{
		ByProject:   sortProjects(s.projects),
		ByModel:     sortModels(s.models),
		Hourly24h:   s.hourly,
		Daily7d:     s.daily,
		Sessions:    len(s.allSessions),
		TopSessions: topSessions(s.sessAgg, 10),
		Heatmap:     s.heatmap,
		WebSearch:   s.webSearch,
		WebFetch:    s.webFetch,
		Edits:       s.edits,
		Reads:       s.reads,
		Bash:        s.bash,
	}, nil
}

// truncatePrompt collapses whitespace in a raw prompt and clips it to w runes,
// so a multi-line last prompt renders as a single tidy title line.
func truncatePrompt(s string, w int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w < 1 {
		return ""
	}
	return string(r[:w-1]) + "…"
}

type pendingEntry struct {
	session string
	model   string
	in, out int64
	cacheR  int64
	cacheC  int64
	webSearch int64
	webFetch  int64
	edits     int
	reads     int
	bash      int
	files     []string // distinct file paths edited in this entry
	ts        time.Time
}

func (s *state) scanFile(f *os.File) {
	var held []pendingEntry
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

		// Capture cwd + branch the first time we see them for a session, and
		// the title/prompt whenever they appear (latest wins — Claude Code
		// rewrites the ai-title as the conversation evolves).
		if e.SessionID != "" {
			if e.CWD != "" {
				if _, ok := s.sessionCWD[e.SessionID]; !ok {
					s.sessionCWD[e.SessionID] = e.CWD
				}
			}
			if e.GitBranch != "" {
				if _, ok := s.sessionBranch[e.SessionID]; !ok {
					s.sessionBranch[e.SessionID] = e.GitBranch
				}
			}
			if e.AITitle != "" {
				s.sessionTitle[e.SessionID] = e.AITitle
			}
			if e.LastPrompt != "" {
				s.sessionPrompt[e.SessionID] = e.LastPrompt
			}
		}

		if e.Type != "assistant" || e.Message.Usage.Input == 0 && e.Message.Usage.Output == 0 {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil || ts.Before(s.prevCutoff) {
			continue
		}

		p := pendingEntry{
			session:   e.SessionID,
			model:     e.Message.Model,
			in:        e.Message.Usage.Input,
			out:       e.Message.Usage.Output,
			cacheR:    e.Message.Usage.CacheReadInput,
			cacheC:    e.Message.Usage.CacheCreationInput,
			webSearch: e.Message.Usage.ServerToolUse.WebSearchRequests,
			webFetch:  e.Message.Usage.ServerToolUse.WebFetchRequests,
			ts:        ts,
		}

		// Count tool actions from the assistant message's content blocks.
		// Errors (e.g. a string content) are ignored — blocks stays empty.
		var blocks []contentBlock
		if json.Unmarshal(e.Message.Content, &blocks) == nil {
			for _, b := range blocks {
				if b.Type != "tool_use" {
					continue
				}
				switch toolKind(b.Name) {
				case "edit":
					p.edits++
					if b.Input.FilePath != "" {
						p.files = append(p.files, b.Input.FilePath)
					}
				case "read":
					p.reads++
				case "bash":
					p.bash++
				}
			}
		}

		cwd, ok := s.sessionCWD[p.session]
		if !ok {
			held = append(held, p)
			continue
		}
		s.apply(p, cwd)
	}
	for _, p := range held {
		cwd := s.sessionCWD[p.session]
		s.apply(p, cwd)
	}
}

func (s *state) apply(p pendingEntry, cwd string) {
	key := cwd
	if key == "" {
		key = "(unknown)"
	}
	pj, ok := s.projects[key]
	if !ok {
		pj = &domain.Project{Path: key, Name: basename(key)}
		s.projects[key] = pj
	}

	tokens := p.in + p.out
	inCurr := p.ts.After(s.cutoff)

	if inCurr {
		pj.In += p.in
		pj.Out += p.out
		pj.CacheR += p.cacheR
		pj.CacheC += p.cacheC
		if ts := p.ts.Unix(); ts > pj.LastAt {
			pj.LastAt = ts
		}
	} else {
		pj.PrevIn += p.in
		pj.PrevOut += p.out
	}

	if p.model != "" {
		m, ok := s.models[p.model]
		if !ok {
			m = &domain.Model{Model: p.model}
			s.models[p.model] = m
		}
		if inCurr {
			m.In += p.in
			m.Out += p.out
			m.CacheR += p.cacheR
			m.CacheC += p.cacheC
			m.WebSearch += p.webSearch
			m.WebFetch += p.webFetch
		} else {
			m.PrevIn += p.in
			m.PrevOut += p.out
		}
	}

	if inCurr {
		s.webSearch += p.webSearch
		s.webFetch += p.webFetch
		s.edits += int64(p.edits)
		s.reads += int64(p.reads)
		s.bash += int64(p.bash)
	}

	if p.session != "" && inCurr {
		s.allSessions[p.session] = struct{}{}
		if s.projSessions[key] == nil {
			s.projSessions[key] = map[string]struct{}{}
		}
		s.projSessions[key][p.session] = struct{}{}
		if p.model != "" {
			if s.modelSessions[p.model] == nil {
				s.modelSessions[p.model] = map[string]struct{}{}
			}
			s.modelSessions[p.model][p.session] = struct{}{}
		}
		// Branch attribution (current period only).
		if br := s.sessionBranch[p.session]; br != "" {
			bkey := branchKey(key, br)
			b, ok := s.branches[bkey]
			if !ok {
				b = &domain.BranchStat{Branch: br}
				s.branches[bkey] = b
			}
			b.In += p.in
			b.Out += p.out
			if s.branchSessions[bkey] == nil {
				s.branchSessions[bkey] = map[string]struct{}{}
			}
			s.branchSessions[bkey][p.session] = struct{}{}
		}
		ts := p.ts.Unix()
		sa, ok := s.sessAgg[p.session]
		if !ok {
			sa = &domain.SessionStat{
				ID:        p.session,
				Project:   basename(key),
				Model:     p.model,
				StartedAt: ts,
				LastAt:    ts,
			}
			s.sessAgg[p.session] = sa
		}
		sa.In += p.in
		sa.Out += p.out
		sa.CacheR += p.cacheR
		sa.CacheC += p.cacheC
		sa.Edits += p.edits
		sa.Reads += p.reads
		sa.Bash += p.bash
		for _, f := range p.files {
			set := s.sessionFiles[p.session]
			if set == nil {
				set = map[string]struct{}{}
				s.sessionFiles[p.session] = set
			}
			set[f] = struct{}{}
		}
		if ts < sa.StartedAt {
			sa.StartedAt = ts
		}
		if ts > sa.LastAt {
			sa.LastAt = ts
		}
	}

	if !inCurr {
		return
	}

	// Time-bucket aggregations (current period only).
	if p.ts.After(s.dayAgo) {
		idx := 23 - int(time.Since(p.ts)/time.Hour)
		if idx < 0 {
			idx = 0
		}
		if idx > 23 {
			idx = 23
		}
		s.hourly[idx] += tokens
	}
	if p.ts.After(s.weekAgo) {
		idx := 6 - int(time.Since(p.ts)/(24*time.Hour))
		if idx < 0 {
			idx = 0
		}
		if idx > 6 {
			idx = 6
		}
		s.daily[idx] += tokens
		// Heatmap: only fill if within last 7d. Day index: Monday=0, Sunday=6.
		// Go's time.Weekday: Sunday=0..Saturday=6, so translate.
		w := int(p.ts.Weekday())
		dayIdx := (w + 6) % 7 // shifts so Monday=0, Tuesday=1, ..., Sunday=6
		hour := p.ts.Hour()
		s.heatmap[dayIdx*24+hour] += tokens
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
		// Skip projects with only previous-period activity (no current tokens).
		if v.Total() == 0 && v.PrevTotal() == 0 {
			continue
		}
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
		if v.Total() == 0 && v.PrevTotal() == 0 {
			continue
		}
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
