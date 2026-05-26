// clawtop is a TUI dashboard for Anthropic Claude usage. It merges per-machine
// status JSON files (written by clawtopd) into a unified view with multiple
// tabs: rate limits, project breakdown, model split, and a 24h sparkline.
//
// Designed to run inside a tmux session on a homelab server, but any terminal
// with at least 60 columns works.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/leonardorifeli/clawtop/internal/domain"
	"github.com/leonardorifeli/clawtop/internal/service/merger"
)

const (
	tabLimits   = 0
	tabProjects = 1
	tabModels   = 2
	tabHourly   = 3
	tabCount    = 4
)

var tabNames = []string{"Limits", "Projects", "Models", "Hourly"}

func main() {
	var (
		dir = flag.String("dir", "/var/lib/clawtop", "directory containing per-machine status JSON files")
	)
	flag.Parse()

	p := tea.NewProgram(initialModel(expandHome(*dir)), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// expandHome resolves a leading "~" or "~/" to the current user's home
// directory. Needed because shells (bash, zsh) do not tilde-expand the
// value side of --flag=value arguments, so users writing
// `clawtop --dir=~/.local/share/clawtop` would otherwise get the literal
// tilde as the path.
func expandHome(p string) string {
	if p == "~" {
		if h, err := os.UserHomeDir(); err == nil {
			return h
		}
	}
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return h + p[1:]
		}
	}
	return p
}

type tickMsg time.Time

type model struct {
	dir      string
	merged   merger.Merged
	loadErr  error
	count    int
	tab      int
	width    int
	height   int
}

func initialModel(dir string) model {
	m := model{dir: dir}
	m.reload()
	return m
}

func (m model) Init() tea.Cmd { return tick() }

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m *model) reload() {
	parts, n, err := loadAll(m.dir)
	m.count = n
	m.loadErr = err
	if err == nil {
		m.merged = merger.Merge(parts)
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			return m, tea.Quit
		case "r":
			m.reload()
			return m, nil
		case "tab", "right", "l":
			m.tab = (m.tab + 1) % tabCount
			return m, nil
		case "shift+tab", "left", "h":
			m.tab = (m.tab + tabCount - 1) % tabCount
			return m, nil
		case "1":
			m.tab = tabLimits
			return m, nil
		case "2":
			m.tab = tabProjects
			return m, nil
		case "3":
			m.tab = tabModels
			return m, nil
		case "4":
			m.tab = tabHourly
			return m, nil
		}
	case tickMsg:
		m.reload()
		return m, tick()
	}
	return m, nil
}

// ---- view -----------------------------------------------------------------

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	pctStyle    = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	activeTab   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Underline(true)
	inactiveTab = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	box         = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(1, 2)
)

func (m model) View() string {
	if m.count == 0 {
		return box.Render(errStyle.Render("no status files in " + m.dir) +
			dimStyle.Render("\nis clawtopd running and pushing here?"))
	}

	w := m.width
	if w < 60 {
		w = 60
	}
	if w > 110 {
		w = 110
	}
	innerW := w - 6

	var body string
	switch m.tab {
	case tabLimits:
		body = viewLimits(m.merged, innerW)
	case tabProjects:
		body = viewProjects(m.merged, innerW)
	case tabModels:
		body = viewModels(m.merged, innerW)
	case tabHourly:
		body = viewHourly(m.merged, innerW)
	}

	header := titleStyle.Render("clawtop") +
		dimStyle.Render("  ·  "+time.Now().Format("15:04:05")) +
		"   " + tabsRow(m.tab)
	footer := footerLine(m.merged, innerW)

	return box.Render(strings.Join([]string{header, "", body, "", footer}, "\n"))
}

func tabsRow(active int) string {
	parts := make([]string, tabCount)
	for i, name := range tabNames {
		s := fmt.Sprintf(" %d %s ", i+1, name)
		if i == active {
			parts[i] = activeTab.Render(s)
		} else {
			parts[i] = inactiveTab.Render(s)
		}
	}
	return strings.Join(parts, dimStyle.Render("·"))
}

func footerLine(m merger.Merged, w int) string {
	hosts := make([]string, len(m.Machines))
	oldest := time.Duration(0)
	now := time.Now()
	for i, mi := range m.Machines {
		hosts[i] = mi.Name
		age := now.Sub(time.Unix(mi.TS, 0))
		if age > oldest {
			oldest = age
		}
	}
	staleness := okStyle.Render("fresh")
	switch {
	case oldest > 5*time.Minute:
		staleness = errStyle.Render("stale " + short(oldest))
	case oldest > 2*time.Minute:
		staleness = warnStyle.Render("stale " + short(oldest))
	}
	left := fmt.Sprintf("hosts: %d (%s)  window: %s  ",
		len(m.Machines), strings.Join(hosts, ","), fallback(m.Window, "?"))
	right := "tab/← → switch · 1-4 jump · r reload · q quit"
	// Pad using lipgloss.Width so ANSI escapes in `staleness` don't throw
	// off the alignment.
	used := lipgloss.Width(left) + lipgloss.Width(staleness) + lipgloss.Width(right)
	gap := w - used
	if gap < 1 {
		gap = 1
	}
	return dimStyle.Render(left) + staleness + strings.Repeat(" ", gap) + dimStyle.Render(right)
}

// ---- tab: limits ----------------------------------------------------------

func viewLimits(m merger.Merged, w int) string {
	barW := w - 12
	lines := []string{
		labelStyle.Render("SESSION  (5h)"),
		bar(m.Session.Pct, barW) + "  " + pct(m.Session.Pct),
		dimStyle.Render("resets in " + short(m.Session.ResetIn())),
		"",
		labelStyle.Render("WEEK     (7d)"),
		bar(m.Week.Pct, barW) + "  " + pct(m.Week.Pct),
		dimStyle.Render("resets in " + short(m.Week.ResetIn())),
		"",
		dimStyle.Render(fmt.Sprintf(
			"plan: %s    limit: %s",
			fallback(m.Subscription, "?"), fallback(m.Limit, "?"))),
	}
	return strings.Join(lines, "\n")
}

// ---- tab: projects --------------------------------------------------------

func viewProjects(m merger.Merged, w int) string {
	if len(m.ByProject) == 0 {
		return dimStyle.Render("no transcript data yet")
	}
	header := labelStyle.Render(fmt.Sprintf("TOP PROJECTS (last %s)", fallback(m.Window, "?")))
	var maxT int64
	for _, p := range m.ByProject {
		if p.Total() > maxT {
			maxT = p.Total()
		}
	}
	nameW := 28
	numW := 12
	barW := w - nameW - numW - 4
	if barW < 10 {
		barW = 10
	}

	lines := []string{header, ""}
	for i, p := range m.ByProject {
		if i >= 10 {
			break
		}
		name := truncate(p.Name, nameW)
		ratio := 0.0
		if maxT > 0 {
			ratio = float64(p.Total()) / float64(maxT) * 100
		}
		lines = append(lines, fmt.Sprintf(
			"%-*s %s  %s",
			nameW, name, bar(ratio, barW), pad(fmtTokens(p.Total()), numW),
		))
	}
	if len(m.ByProject) > 10 {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("... and %d more", len(m.ByProject)-10)))
	}
	return strings.Join(lines, "\n")
}

// ---- tab: models ----------------------------------------------------------

func viewModels(m merger.Merged, w int) string {
	if len(m.ByModel) == 0 {
		return dimStyle.Render("no transcript data yet")
	}
	header := labelStyle.Render(fmt.Sprintf("MODELS (last %s)", fallback(m.Window, "?")))
	var maxT int64
	for _, mm := range m.ByModel {
		if mm.Total() > maxT {
			maxT = mm.Total()
		}
	}
	nameW := 22
	numW := 12
	barW := w - nameW - numW - 4
	if barW < 10 {
		barW = 10
	}

	lines := []string{header, ""}
	for _, mm := range m.ByModel {
		ratio := 0.0
		if maxT > 0 {
			ratio = float64(mm.Total()) / float64(maxT) * 100
		}
		lines = append(lines, fmt.Sprintf(
			"%-*s %s  %s",
			nameW, truncate(prettyModel(mm.Model), nameW),
			bar(ratio, barW), pad(fmtTokens(mm.Total()), numW),
		))
		lines = append(lines, dimStyle.Render(fmt.Sprintf(
			"%-*s  in=%s  out=%s  cache_read=%s  cache_create=%s",
			nameW, "",
			fmtTokens(mm.In), fmtTokens(mm.Out),
			fmtTokens(mm.CacheR), fmtTokens(mm.CacheC),
		)))
	}
	return strings.Join(lines, "\n")
}

// ---- tab: hourly ----------------------------------------------------------

func viewHourly(m merger.Merged, w int) string {
	header := labelStyle.Render("LAST 24h (tokens per hour, all machines combined)")
	if len(m.Hourly24h) == 0 {
		return strings.Join([]string{header, "", dimStyle.Render("no transcript data yet")}, "\n")
	}
	spark := sparkline(m.Hourly24h, w-2)
	var sum int64
	for _, v := range m.Hourly24h {
		sum += v
	}
	scaleRow := dimStyle.Render(fmt.Sprintf("24h total: %s  ·  peak hour: %s",
		fmtTokens(sum), fmtTokens(maxInt64(m.Hourly24h))))
	axisRow := dimStyle.Render(padRight("-24h", w/2) + padRight("-12h", w/2-4) + "now")
	return strings.Join([]string{header, "", spark, axisRow, "", scaleRow}, "\n")
}

// ---- helpers --------------------------------------------------------------

func bar(pct float64, width int) string {
	if width < 4 {
		width = 4
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(float64(width) * pct / 100)
	if filled > width {
		filled = width
	}
	color := lipgloss.Color("82")
	switch {
	case pct >= 90:
		color = lipgloss.Color("196")
	case pct >= 75:
		color = lipgloss.Color("214")
	case pct >= 50:
		color = lipgloss.Color("226")
	}
	full := lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", filled))
	rest := dimStyle.Render(strings.Repeat("░", width-filled))
	return full + rest
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

func sparkline(values []int64, width int) string {
	if width < len(values) {
		// Compress: each output column is the average of N values.
		out := make([]int64, width)
		group := len(values) / width
		if group < 1 {
			group = 1
		}
		for i := 0; i < width; i++ {
			start := i * group
			end := start + group
			if end > len(values) {
				end = len(values)
			}
			var s int64
			for j := start; j < end; j++ {
				s += values[j]
			}
			out[i] = s
		}
		values = out
	} else if width > len(values) {
		// Stretch: each input fills W/N columns.
		stretch := width / len(values)
		out := make([]int64, 0, width)
		for _, v := range values {
			for j := 0; j < stretch; j++ {
				out = append(out, v)
			}
		}
		for len(out) < width {
			out = append(out, values[len(values)-1])
		}
		values = out
	}

	max := maxInt64(values)
	if max == 0 {
		return dimStyle.Render(strings.Repeat("▁", width))
	}
	var b strings.Builder
	for _, v := range values {
		idx := int(float64(v) / float64(max) * float64(len(sparkRunes)-1))
		if idx < 0 {
			idx = 0
		}
		if idx > len(sparkRunes)-1 {
			idx = len(sparkRunes) - 1
		}
		b.WriteRune(sparkRunes[idx])
	}
	return okStyle.Render(b.String())
}

func pct(v float64) string {
	return pctStyle.Render(fmt.Sprintf("%5.1f%%", v))
}

func short(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		days := int(d.Hours()) / 24
		return fmt.Sprintf("%dd %dh", days, int(d.Hours())%24)
	}
}

func fallback(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func fmtTokens(n int64) string {
	switch {
	case n >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(n)/1_000_000_000)
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

func prettyModel(m string) string {
	// "claude-opus-4-7" → "opus-4-7"; "claude-sonnet-4-6" → "sonnet-4-6".
	if strings.HasPrefix(m, "claude-") {
		return strings.TrimPrefix(m, "claude-")
	}
	return m
}

func truncate(s string, w int) string {
	if len(s) <= w {
		return s
	}
	if w < 1 {
		return ""
	}
	return s[:w-1] + "…"
}

func pad(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return strings.Repeat(" ", w-len(s)) + s
}

func padRight(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

func maxInt64(xs []int64) int64 {
	var m int64
	for _, x := range xs {
		if x > m {
			m = x
		}
	}
	return m
}

func loadAll(dir string) ([]domain.Status, int, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return nil, 0, err
	}
	sort.Strings(matches)
	out := make([]domain.Status, 0, len(matches))
	for _, p := range matches {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var s domain.Status
		if err := json.Unmarshal(b, &s); err != nil {
			continue
		}
		if s.Machine == "" {
			s.Machine = strings.TrimSuffix(filepath.Base(p), ".json")
		}
		out = append(out, s)
	}
	return out, len(out), nil
}
