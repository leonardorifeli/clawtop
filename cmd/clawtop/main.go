// clawtop is a TUI dashboard for Anthropic Claude usage. It merges per-machine
// status JSON files (written by clawtopd) into a unified view.
//
// Default layout is a single-screen dashboard (limits, projects, models, and a
// 24h sparkline visible at once). On narrow or short terminals it falls back
// to a tabbed layout, switchable with the `t` key.
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
	tabHosts    = 3
	tabHourly   = 4
	tabCount    = 5
)

var tabNames = []string{"Limits", "Projects", "Models", "Hosts", "Hourly"}

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
// value side of --flag=value arguments.
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
	dir             string
	merged          merger.Merged
	loadErr         error
	count           int
	tab             int
	width           int
	height          int
	forceTabbed     bool // user toggled to tabbed even though dashboard would fit
	forceDashboard  bool // user toggled to dashboard even though terminal is small
	scrollProjects  int
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
	// Re-clamp scroll in case the project list shrank.
	maxScroll := m.maxProjectScroll(m.projectsVisible())
	if m.scrollProjects > maxScroll {
		m.scrollProjects = maxScroll
	}
}

// dashboardFits returns true when the terminal is big enough for the
// single-screen layout. The threshold is intentionally loose: tighter than
// this, the dashboard would overflow vertically and dropping back to the
// tabbed layout gives a cleaner read.
func (m model) dashboardFits() bool {
	return m.width >= 80 && m.height >= 18
}

func (m model) useDashboard() bool {
	if m.forceTabbed {
		return false
	}
	if m.forceDashboard {
		return true
	}
	return m.dashboardFits()
}

// projectsVisible returns how many project rows fit in the current layout.
// Each project may add a sub-line (sessions or host attribution), so we
// account for the worst case to avoid overflow.
func (m model) projectsVisible() int {
	if m.useDashboard() {
		// Dashboard reserves: header (1), limits (2), 2 rules, hosts (2-4),
		// hourly+daily (2), help (1), plus blank lines between sections.
		// Each project line takes 2 rows (name+sub-line).
		n := (m.height - 14) / 2
		if n < 2 {
			return 2
		}
		return n
	}
	n := (m.height - 6) / 2
	if n < 3 {
		return 3
	}
	return n
}

func (m model) maxProjectScroll(visible int) int {
	n := len(m.merged.ByProject) - visible
	if n < 0 {
		return 0
	}
	return n
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
		case "t":
			// Toggle layout mode and remember the user's intent.
			if m.useDashboard() {
				m.forceTabbed, m.forceDashboard = true, false
			} else {
				m.forceTabbed, m.forceDashboard = false, true
			}
			return m, nil
		case "tab", "right", "l":
			if !m.useDashboard() {
				m.tab = (m.tab + 1) % tabCount
			}
			return m, nil
		case "shift+tab", "left", "h":
			if !m.useDashboard() {
				m.tab = (m.tab + tabCount - 1) % tabCount
			}
			return m, nil
		case "1", "2", "3", "4", "5":
			if !m.useDashboard() {
				m.tab = int(msg.String()[0] - '1')
			}
			return m, nil
		case "j", "down":
			vis := m.projectsVisible()
			if m.scrollProjects < m.maxProjectScroll(vis) {
				m.scrollProjects++
			}
			return m, nil
		case "k", "up":
			if m.scrollProjects > 0 {
				m.scrollProjects--
			}
			return m, nil
		case "g":
			m.scrollProjects = 0
			return m, nil
		case "G":
			m.scrollProjects = m.maxProjectScroll(m.projectsVisible())
			return m, nil
		case "pgdown", " ":
			vis := m.projectsVisible()
			m.scrollProjects += vis
			if max := m.maxProjectScroll(vis); m.scrollProjects > max {
				m.scrollProjects = max
			}
			return m, nil
		case "pgup":
			vis := m.projectsVisible()
			m.scrollProjects -= vis
			if m.scrollProjects < 0 {
				m.scrollProjects = 0
			}
			return m, nil
		}
	case tickMsg:
		m.reload()
		return m, tick()
	}
	return m, nil
}

// ---- styles ---------------------------------------------------------------

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	labelStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	pctStyle    = lipgloss.NewStyle().Bold(true)
	dimStyle    = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("82"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	neutralBar  = lipgloss.NewStyle().Foreground(lipgloss.Color("39")) // cyan
	activeTab   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")).Underline(true)
	inactiveTab = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	padding     = lipgloss.NewStyle().PaddingLeft(1).PaddingRight(1)
)

func (m model) View() string {
	if m.count == 0 {
		return padding.Render(errStyle.Render("no status files in "+m.dir) + "\n" +
			dimStyle.Render("is clawtopd running and pushing here?"))
	}
	if m.useDashboard() {
		return m.viewDashboard()
	}
	return m.viewTabbed()
}

// ---- dashboard view (default) ---------------------------------------------

func (m model) viewDashboard() string {
	w := m.width
	if w > 200 {
		w = 200
	}
	innerW := w - 2 // padding(1, 1)

	merged := m.merged
	rule := dimStyle.Render(strings.Repeat("─", innerW))

	// Compact status header in one line.
	header := dashboardHeader(merged, innerW)

	// Limits: just two bars + reset, plan/limit goes into header line.
	limits := dashboardLimits(merged, innerW)

	// Projects + Models side by side.
	leftW := innerW * 60 / 100
	rightW := innerW - leftW - 2
	if leftW < 38 {
		leftW = 38
	}
	if rightW < 24 {
		rightW = innerW - leftW - 2
		if rightW < 24 {
			rightW = 24
		}
	}
	visible := m.projectsVisible()
	leftCol := dashboardProjects(merged, leftW, visible, m.scrollProjects)
	rightCol := dashboardModels(merged, rightW)
	cols := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol)

	hosts := dashboardHosts(merged, innerW)
	trends := dashboardTrends(merged, innerW)

	keys := dimStyle.Render("t tabbed · j/k scroll · g/G top/end · r reload · q quit")

	body := strings.Join([]string{
		header,
		rule,
		limits,
		rule,
		cols,
		rule,
		hosts,
		rule,
		trends,
		keys,
	}, "\n")
	return padding.Render(body)
}

// dashboardHeader is a single status line: title, time, account-level KPIs,
// hosts count, freshness. Saves several rows vs the v0.5 layout.
func dashboardHeader(m merger.Merged, w int) string {
	left := titleStyle.Render("clawtop") +
		dimStyle.Render("  ·  "+time.Now().Format("15:04:05")+
			"  ·  hosts "+fmt.Sprintf("%d (%s)", len(m.Machines), joinHosts(m.Machines))+
			"  ·  plan "+fallback(m.Subscription, "?")+
			"  ·  window "+fallback(m.Window, "?")+"  ")
	right := freshnessLabel(m.Machines)
	gap := w - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// dashboardHosts renders a one-line-per-machine summary inline with the
// section header, so a single-host setup takes 1 row total.
func dashboardHosts(m merger.Merged, w int) string {
	if len(m.Machines) == 0 {
		return labelStyle.Render("HOSTS") + " " + dimStyle.Render("none")
	}
	now := time.Now()
	parts := []string{labelStyle.Render("HOSTS")}
	for _, mi := range m.Machines {
		age := now.Sub(time.Unix(mi.TS, 0))
		fresh := okStyle.Render("fresh")
		switch {
		case age > 5*time.Minute:
			fresh = errStyle.Render(short(age))
		case age > 2*time.Minute:
			fresh = warnStyle.Render(short(age))
		}
		parts = append(parts, fmt.Sprintf("%s %s·%dp·%ds·%s",
			mi.Name, fmtTokens(mi.Total), mi.Projects, mi.Sessions, fresh))
	}
	// If many machines, split across lines.
	line := strings.Join(parts, "  ")
	if lipgloss.Width(line) <= w {
		return line
	}
	// Fall back: header on its own row, machines below indented.
	rows := []string{labelStyle.Render("HOSTS")}
	for _, mi := range m.Machines {
		age := now.Sub(time.Unix(mi.TS, 0))
		fresh := okStyle.Render("fresh")
		switch {
		case age > 5*time.Minute:
			fresh = errStyle.Render(short(age))
		case age > 2*time.Minute:
			fresh = warnStyle.Render(short(age))
		}
		rows = append(rows, fmt.Sprintf("  %-16s %s · %dp · %ds · %s",
			truncate(mi.Name, 16), pad(fmtTokens(mi.Total), 7), mi.Projects, mi.Sessions, fresh))
	}
	return strings.Join(rows, "\n")
}

// dashboardTrends shows 24h and 7d sparklines stacked, each on a single line
// with its totals on the right.
func dashboardTrends(m merger.Merged, w int) string {
	hourly := trendRow("24h", m.Hourly24h, w)
	daily := trendRow(" 7d", m.Daily7d, w)
	return strings.Join([]string{hourly, daily}, "\n")
}

func trendRow(label string, vals []int64, w int) string {
	if len(vals) == 0 {
		return labelStyle.Render(label) + " " + dimStyle.Render("no data")
	}
	var sum int64
	for _, v := range vals {
		sum += v
	}
	totals := dimStyle.Render(fmt.Sprintf("%s · peak %s",
		fmtTokens(sum), fmtTokens(maxInt64(vals))))
	barW := w - lipgloss.Width(totals) - 7 // label + spacing
	if barW < 10 {
		barW = 10
	}
	return labelStyle.Render(label) + " " + sparkline(vals, barW) + "  " + totals
}

// hostsLine renders a compact per-host attribution line like
// "omen 800k · laptop 100k". Truncated to fit w.
func hostsLine(hosts []merger.HostContribution, w int) string {
	parts := make([]string, 0, len(hosts))
	for _, h := range hosts {
		parts = append(parts, fmt.Sprintf("%s %s", h.Host, fmtTokens(h.Total())))
	}
	s := strings.Join(parts, " · ")
	if len(s) > w {
		s = s[:w-1] + "…"
	}
	return s
}

func dashboardLimits(m merger.Merged, w int) string {
	barW := w - 32
	if barW < 10 {
		barW = 10
	}
	return strings.Join([]string{
		fmt.Sprintf("%-8s %s  %s  %s",
			labelStyle.Render("SESSION"),
			barRanked(m.Session.Pct, barW),
			pct(m.Session.Pct),
			dimStyle.Render("· resets "+short(m.Session.ResetIn()))),
		fmt.Sprintf("%-8s %s  %s  %s",
			labelStyle.Render("WEEK"),
			barRanked(m.Week.Pct, barW),
			pct(m.Week.Pct),
			dimStyle.Render("· resets "+short(m.Week.ResetIn()))),
	}, "\n")
}

func dashboardProjects(m merger.Merged, w, visible, scroll int) string {
	header := labelStyle.Render(fmt.Sprintf("TOP PROJECTS (last %s)", fallback(m.Window, "?")))
	if len(m.ByProject) == 0 {
		return strings.Join([]string{header, "", dimStyle.Render("no transcript data yet")}, "\n")
	}

	var maxT int64
	for _, p := range m.ByProject {
		if p.Total() > maxT {
			maxT = p.Total()
		}
	}
	nameW := w * 45 / 100
	if nameW < 16 {
		nameW = 16
	}
	numW := 10
	barW := w - nameW - numW - 2
	if barW < 6 {
		barW = 6
	}

	rows := []string{header, ""}
	end := scroll + visible
	if end > len(m.ByProject) {
		end = len(m.ByProject)
	}
	for i := scroll; i < end; i++ {
		p := m.ByProject[i]
		ratio := 0.0
		if maxT > 0 {
			ratio = float64(p.Total()) / float64(maxT) * 100
		}
		rows = append(rows, fmt.Sprintf("%-*s %s %s",
			nameW, truncate(p.Name, nameW),
			pad(fmtTokens(p.Total()), numW),
			barNeutral(ratio, barW),
		))
		// Host attribution sub-line only when 2+ hosts contributed.
		if hosts, ok := m.HostsByProject[p.Path]; ok {
			rows = append(rows, dimStyle.Render("  "+hostsLine(hosts, w-2)))
		} else if p.Sessions > 0 {
			rows = append(rows, dimStyle.Render(fmt.Sprintf("  %d sessions", p.Sessions)))
		}
	}

	hint := ""
	if scroll > 0 && end < len(m.ByProject) {
		hint = fmt.Sprintf("▲ %d above · %d below ▼", scroll, len(m.ByProject)-end)
	} else if scroll > 0 {
		hint = fmt.Sprintf("▲ %d above", scroll)
	} else if end < len(m.ByProject) {
		hint = fmt.Sprintf("%d below ▼", len(m.ByProject)-end)
	}
	if hint != "" {
		rows = append(rows, dimStyle.Render(hint))
	}
	return strings.Join(rows, "\n")
}

func dashboardModels(m merger.Merged, w int) string {
	header := labelStyle.Render("MODELS")
	if len(m.ByModel) == 0 {
		return strings.Join([]string{header, dimStyle.Render("no data")}, "\n")
	}
	rows := []string{header}
	for _, mm := range m.ByModel {
		nameW := w - 11
		rows = append(rows,
			fmt.Sprintf("%-*s %s",
				nameW, truncate(prettyModel(mm.Model), nameW),
				pad(fmtTokens(mm.Total()), 9)),
			dimStyle.Render(fmt.Sprintf("  in %s · out %s · cache %.0f%% · %d sess",
				fmtTokens(mm.In), fmtTokens(mm.Out), mm.CacheHitRate(), mm.Sessions)),
		)
	}
	return strings.Join(rows, "\n")
}

// ---- tabbed view (fallback for small terminals) ---------------------------

func (m model) viewTabbed() string {
	w := m.width
	if w < 60 {
		w = 60
	}
	if w > 110 {
		w = 110
	}
	innerW := w - 2

	var body string
	switch m.tab {
	case tabLimits:
		body = viewLimits(m.merged, innerW)
	case tabProjects:
		body = viewProjects(m.merged, innerW, m.projectsVisible(), m.scrollProjects)
	case tabModels:
		body = viewModels(m.merged, innerW)
	case tabHosts:
		body = viewHosts(m.merged, innerW)
	case tabHourly:
		body = viewHourly(m.merged, innerW)
	}

	header := titleStyle.Render("clawtop") +
		dimStyle.Render("  ·  "+time.Now().Format("15:04:05")) +
		"   " + tabsRow(m.tab)
	footer := footerLine(m.merged, innerW)

	return padding.Render(strings.Join([]string{header, "", body, "", footer}, "\n"))
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
	left := fmt.Sprintf("hosts: %d (%s)  window: %s  ",
		len(m.Machines), joinHosts(m.Machines), fallback(m.Window, "?"))
	staleness := freshnessLabel(m.Machines)
	right := "tab/1-5 nav · t dash · r reload · q quit"
	used := lipgloss.Width(left) + lipgloss.Width(staleness) + lipgloss.Width(right)
	gap := w - used
	if gap < 1 {
		gap = 1
	}
	return dimStyle.Render(left) + staleness + strings.Repeat(" ", gap) + dimStyle.Render(right)
}

func viewLimits(m merger.Merged, w int) string {
	barW := w - 12
	return strings.Join([]string{
		labelStyle.Render("SESSION  (5h)"),
		barRanked(m.Session.Pct, barW) + "  " + pct(m.Session.Pct),
		dimStyle.Render("resets in " + short(m.Session.ResetIn())),
		"",
		labelStyle.Render("WEEK     (7d)"),
		barRanked(m.Week.Pct, barW) + "  " + pct(m.Week.Pct),
		dimStyle.Render("resets in " + short(m.Week.ResetIn())),
		"",
		dimStyle.Render("plan: " + fallback(m.Subscription, "?") + "    limit: " + fallback(m.Limit, "?")),
	}, "\n")
}

func viewProjects(m merger.Merged, w, visible, scroll int) string {
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
	rows := []string{header, ""}
	end := scroll + visible
	if end > len(m.ByProject) {
		end = len(m.ByProject)
	}
	for i := scroll; i < end; i++ {
		p := m.ByProject[i]
		ratio := 0.0
		if maxT > 0 {
			ratio = float64(p.Total()) / float64(maxT) * 100
		}
		rows = append(rows, fmt.Sprintf("%-*s %s  %s",
			nameW, truncate(p.Name, nameW),
			barNeutral(ratio, barW),
			pad(fmtTokens(p.Total()), numW),
		))
		if hosts, ok := m.HostsByProject[p.Path]; ok {
			rows = append(rows, dimStyle.Render("  "+hostsLine(hosts, w-2)))
		} else if p.Sessions > 0 {
			rows = append(rows, dimStyle.Render(fmt.Sprintf("  %d sessions", p.Sessions)))
		}
	}
	if scroll > 0 || end < len(m.ByProject) {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("showing %d-%d of %d · j/k g G PgUp/PgDn",
			scroll+1, end, len(m.ByProject))))
	}
	return strings.Join(rows, "\n")
}

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
	rows := []string{header, ""}
	for _, mm := range m.ByModel {
		ratio := 0.0
		if maxT > 0 {
			ratio = float64(mm.Total()) / float64(maxT) * 100
		}
		rows = append(rows,
			fmt.Sprintf("%-*s %s  %s",
				nameW, truncate(prettyModel(mm.Model), nameW),
				barNeutral(ratio, barW),
				pad(fmtTokens(mm.Total()), numW)),
			dimStyle.Render(fmt.Sprintf("%-*s  in=%s  out=%s  cache_read=%s  cache_create=%s",
				nameW, "",
				fmtTokens(mm.In), fmtTokens(mm.Out),
				fmtTokens(mm.CacheR), fmtTokens(mm.CacheC))),
			dimStyle.Render(fmt.Sprintf("%-*s  cache_hit=%.0f%%  sessions=%d",
				nameW, "", mm.CacheHitRate(), mm.Sessions)),
		)
	}
	return strings.Join(rows, "\n")
}

// viewHosts renders the per-machine table: name, freshness, total tokens
// contributed, distinct projects, distinct sessions. Useful to spot who is
// doing the heavy lifting and who has gone quiet.
func viewHosts(m merger.Merged, w int) string {
	header := labelStyle.Render(fmt.Sprintf("HOSTS (last %s)", fallback(m.Window, "?")))
	if len(m.Machines) == 0 {
		return strings.Join([]string{header, "", dimStyle.Render("no hosts reporting")}, "\n")
	}
	now := time.Now()
	rows := []string{header, "",
		dimStyle.Render(fmt.Sprintf("%-18s %-10s %-10s %-10s %s", "name", "tokens", "projects", "sessions", "freshness")),
	}
	for _, mi := range m.Machines {
		age := now.Sub(time.Unix(mi.TS, 0))
		fresh := okStyle.Render("fresh")
		switch {
		case age > 5*time.Minute:
			fresh = errStyle.Render(short(age))
		case age > 2*time.Minute:
			fresh = warnStyle.Render(short(age))
		}
		rows = append(rows, fmt.Sprintf("%-18s %-10s %-10d %-10d %s",
			truncate(mi.Name, 18),
			fmtTokens(mi.Total),
			mi.Projects,
			mi.Sessions,
			fresh,
		))
	}
	return strings.Join(rows, "\n")
}

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

// ---- shared helpers -------------------------------------------------------

// barRanked colors the bar by percentage (green→yellow→orange→red). Use for
// rate-limit windows where high values are bad.
func barRanked(pct float64, width int) string {
	filled, rest := barCells(pct, width)
	color := lipgloss.Color("82")
	switch {
	case pct >= 90:
		color = lipgloss.Color("196")
	case pct >= 75:
		color = lipgloss.Color("214")
	case pct >= 50:
		color = lipgloss.Color("226")
	}
	return lipgloss.NewStyle().Foreground(color).Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", rest))
}

// barNeutral is a single-color bar suitable for relative comparisons (project
// rank, model split) where the value doesn't carry good/bad semantics.
func barNeutral(pct float64, width int) string {
	filled, rest := barCells(pct, width)
	return neutralBar.Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("░", rest))
}

func barCells(pct float64, width int) (filled, rest int) {
	if width < 4 {
		width = 4
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled = int(float64(width) * pct / 100)
	if filled > width {
		filled = width
	}
	rest = width - filled
	return
}

var sparkRunes = []rune("▁▂▃▄▅▆▇█")

func sparkline(values []int64, width int) string {
	if width < 4 {
		width = 4
	}
	if width < len(values) {
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
	return neutralBar.Render(b.String())
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

func joinHosts(ms []merger.MachineInfo) string {
	names := make([]string, len(ms))
	for i, m := range ms {
		names[i] = m.Name
	}
	return strings.Join(names, ",")
}

func freshnessLabel(ms []merger.MachineInfo) string {
	oldest := time.Duration(0)
	now := time.Now()
	for _, mi := range ms {
		age := now.Sub(time.Unix(mi.TS, 0))
		if age > oldest {
			oldest = age
		}
	}
	switch {
	case oldest > 5*time.Minute:
		return errStyle.Render("stale " + short(oldest))
	case oldest > 2*time.Minute:
		return warnStyle.Render("stale " + short(oldest))
	default:
		return okStyle.Render("fresh")
	}
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
