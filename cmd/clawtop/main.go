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
	"strconv"
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
	tabSessions = 4
	tabHeatmap  = 5
	tabHourly   = 6
	tabCount    = 7
)

var tabNames = []string{"Limits", "Projects", "Models", "Hosts", "Sessions", "Heatmap", "Hourly"}

func main() {
	var (
		dir     = flag.String("dir", "/var/lib/clawtop", "directory containing per-machine status JSON files")
		machine = flag.String("machine", "", "show only this machine (filename without .json); empty = show all merged")
		pricing = flag.String("pricing", "", "optional JSON file overriding the built-in per-million-token USD price estimates")
	)
	flag.Parse()

	p := tea.NewProgram(initialModel(expandHome(*dir), *machine, loadPricing(expandHome(*pricing))), tea.WithAltScreen())
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
	machineFilter   string // "" = all machines merged
	availableHosts  []string
	merged          merger.Merged
	loadErr         error
	count           int
	tab             int
	width           int
	height          int
	forceTabbed     bool
	forceDashboard  bool
	scrollProjects  int
	history         []probeSample // last ~20 probes, for burn-rate calc
	pricing         pricingTable  // per-million-token USD estimates
}

// probeSample records one observation of the rate-limit windows over time.
// We retain a small rolling window to derive a per-hour burn rate.
type probeSample struct {
	at         time.Time
	sessionPct float64
	weekPct    float64
}

const historyKeep = 30 // ~30 minutes at 1 sample/min equivalent

func initialModel(dir, filter string, pricing pricingTable) model {
	m := model{dir: dir, machineFilter: filter, pricing: pricing}
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
		m.availableHosts = m.availableHosts[:0]
		for _, p := range parts {
			m.availableHosts = append(m.availableHosts, p.Machine)
		}
		filtered := parts
		if m.machineFilter != "" {
			filtered = filtered[:0]
			for _, p := range parts {
				if p.Machine == m.machineFilter {
					filtered = append(filtered, p)
				}
			}
		}
		m.merged = merger.Merge(filtered)
		// Record a sample for burn-rate; skip if percentages are zero (probe
		// failed and we have no value to project from yet).
		if m.merged.Session.Pct > 0 || m.merged.Week.Pct > 0 {
			m.history = append(m.history, probeSample{
				at:         time.Unix(m.merged.TS, 0),
				sessionPct: m.merged.Session.Pct,
				weekPct:    m.merged.Week.Pct,
			})
			if len(m.history) > historyKeep {
				m.history = m.history[len(m.history)-historyKeep:]
			}
		}
	}
	maxScroll := m.maxProjectScroll(m.projectsVisible())
	if m.scrollProjects > maxScroll {
		m.scrollProjects = maxScroll
	}
}

// burnRate returns the percentage-per-hour rate for the two windows,
// computed from history. Returns 0,0 when we don't yet have two distinct
// samples or when both values are flat.
func (m model) burnRate() (sessionRate, weekRate float64) {
	if len(m.history) < 2 {
		return 0, 0
	}
	first := m.history[0]
	last := m.history[len(m.history)-1]
	dt := last.at.Sub(first.at).Hours()
	if dt <= 0 {
		return 0, 0
	}
	dsess := last.sessionPct - first.sessionPct
	dweek := last.weekPct - first.weekPct
	if dsess < 0 {
		dsess = 0 // a reset crossed the window; don't show negative
	}
	if dweek < 0 {
		dweek = 0
	}
	return dsess / dt, dweek / dt
}

// projectionLine builds the burn-rate suffix for one limit window.
// Returns empty string if no useful projection (no samples or flat).
func projectionLine(pct float64, ratePerHour float64, resetIn time.Duration) string {
	if ratePerHour <= 0 || pct >= 100 {
		return ""
	}
	hoursToCap := (100 - pct) / ratePerHour
	d := time.Duration(hoursToCap * float64(time.Hour))
	tag := okStyle.Render("ok")
	if resetIn > 0 && d < resetIn {
		tag = errStyle.Render("OVER!")
	} else if resetIn > 0 && d < resetIn+30*time.Minute {
		tag = warnStyle.Render("close")
	}
	return fmt.Sprintf(" · +%.1f%%/h · 100%% in %s · %s",
		ratePerHour, short(d), tag)
}

// cycleFilter advances machineFilter through "", host1, host2, ..., back to "".
func (m *model) cycleFilter() {
	if len(m.availableHosts) <= 1 {
		m.machineFilter = ""
		return
	}
	if m.machineFilter == "" {
		m.machineFilter = m.availableHosts[0]
		return
	}
	for i, h := range m.availableHosts {
		if h == m.machineFilter {
			if i+1 < len(m.availableHosts) {
				m.machineFilter = m.availableHosts[i+1]
			} else {
				m.machineFilter = ""
			}
			return
		}
	}
	m.machineFilter = ""
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
		case "f":
			m.cycleFilter()
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
		case "1", "2", "3", "4", "5", "6", "7":
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
	header := m.dashboardHeader(innerW)

	// Limits: just two bars + reset, plan/limit goes into header line.
	limits := m.dashboardLimits(innerW)

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
	rightCol := dashboardModels(merged, rightW, m.pricing)
	cols := lipgloss.JoinHorizontal(lipgloss.Top, leftCol, "  ", rightCol)

	hosts := dashboardHosts(merged, innerW)
	sessions := dashboardSessions(merged, innerW, m.pricing)
	trends := dashboardTrends(merged, innerW)

	keys := dimStyle.Render("t tabbed · f filter host · j/k scroll · g/G top/end · r reload · q quit")

	body := strings.Join([]string{
		header,
		rule,
		limits,
		rule,
		cols,
		rule,
		hosts,
		rule,
		sessions,
		rule,
		trends,
		keys,
	}, "\n")
	return padding.Render(body)
}

// dashboardSessions is a compact view of the top 3 most expensive sessions in
// the window: live dot + title, with tokens/actions/model/age as dim meta.
func dashboardSessions(m merger.Merged, w int, pricing pricingTable) string {
	header := labelStyle.Render("TOP SESSIONS")
	if a := actionsSummary(m); a != "" {
		header += dimStyle.Render(" · " + a)
	}
	if len(m.TopSessions) == 0 {
		return header + " " + dimStyle.Render("none")
	}
	labelW := w * 40 / 100
	if labelW < 16 {
		labelW = 16
	}
	rows := []string{header}
	now := time.Now()
	for i, s := range m.TopSessions {
		if i >= 3 {
			break
		}
		label := s.Title
		if label == "" {
			label = s.Project
		}
		meta := []string{fmtTokens(s.Total())}
		if c, ok := pricing.sessionCost(s); ok {
			meta = append(meta, fmtUSD(c))
		}
		if act := sessionActions(s); act != "" {
			meta = append(meta, act)
		}
		meta = append(meta, prettyModel(s.Model), short(now.Sub(time.Unix(s.LastAt, 0)))+" ago")
		marker := " "
		if isSpinning(s) {
			marker = warnStyle.Render("⚠")
		}
		rows = append(rows, fmt.Sprintf("  %s %-*s %s %s",
			liveDot(s.LastAt),
			labelW, truncate(label, labelW),
			marker,
			dimStyle.Render(truncate(strings.Join(meta, " · "), w-labelW-7)),
		))
	}
	return strings.Join(rows, "\n")
}

// dashboardHeader is a single status line: title, time, account-level KPIs,
// hosts count, optional filter, freshness.
func (m model) dashboardHeader(w int) string {
	merged := m.merged
	filterPart := ""
	if m.machineFilter != "" {
		filterPart = "  ·  filter " + warnStyle.Render(m.machineFilter)
	}
	livePart := ""
	if n := liveCount(merged); n > 0 {
		livePart = dimStyle.Render("  ·  ") + okStyle.Render(fmt.Sprintf("%d live", n))
	}
	left := titleStyle.Render("clawtop") +
		dimStyle.Render("  ·  "+time.Now().Format("15:04:05")+
			"  ·  hosts "+fmt.Sprintf("%d (%s)", len(merged.Machines), joinHosts(merged.Machines))+
			"  ·  plan "+fallback(merged.Subscription, "?")+
			"  ·  window "+fallback(merged.Window, "?")) + livePart + filterPart + dimStyle.Render("  ")
	right := freshnessLabel(merged.Machines)
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

func (m model) dashboardLimits(w int) string {
	merged := m.merged
	sessR, weekR := m.burnRate()
	barW := w - 32
	if barW < 10 {
		barW = 10
	}
	sessDelta, weekDelta := "", ""
	if merged.HasHistory {
		sessDelta = "  " + dayDeltaLabel(merged.Session)
		weekDelta = "  " + dayDeltaLabel(merged.Week)
	}
	return strings.Join([]string{
		fmt.Sprintf("%-8s %s  %s  %s%s",
			labelStyle.Render("SESSION"),
			barRanked(merged.Session.Pct, barW),
			pct(merged.Session.Pct),
			dimStyle.Render("· resets "+short(merged.Session.ResetIn())+
				projectionLine(merged.Session.Pct, sessR, merged.Session.ResetIn())),
			sessDelta),
		fmt.Sprintf("%-8s %s  %s  %s%s",
			labelStyle.Render("WEEK"),
			barRanked(merged.Week.Pct, barW),
			pct(merged.Week.Pct),
			dimStyle.Render("· resets "+short(merged.Week.ResetIn())+
				projectionLine(merged.Week.Pct, weekR, merged.Week.ResetIn())),
			weekDelta),
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

func dashboardModels(m merger.Merged, w int, pricing pricingTable) string {
	header := labelStyle.Render("MODELS")
	if total := pricing.totalCost(m.ByModel); total > 0 {
		suffix := " · est. " + fmtUSD(total)
		if mo, ok := monthlyCost(total, m.Window); ok {
			suffix += " · ~" + fmtUSD(mo) + "/mo"
		}
		header += dimStyle.Render(suffix)
	}
	if len(m.ByModel) == 0 {
		return strings.Join([]string{header, dimStyle.Render("no data")}, "\n")
	}
	rows := []string{header}
	for _, mm := range m.ByModel {
		nameW := w - 11
		costStr := ""
		if c, ok := pricing.modelCost(mm); ok {
			costStr = " · " + fmtUSD(c)
		}
		rows = append(rows,
			fmt.Sprintf("%-*s %s",
				nameW, truncate(prettyModel(mm.Model), nameW),
				pad(fmtTokens(mm.Total()), 9)),
			dimStyle.Render(fmt.Sprintf("  in %s · out %s · cache %.0f%% (saved ~%s) · %d sess%s",
				fmtTokens(mm.In), fmtTokens(mm.Out), mm.CacheHitRate(),
				fmtTokens(int64(float64(mm.CacheR)*0.9)), mm.Sessions, costStr)),
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
		body = m.viewLimits(innerW)
	case tabProjects:
		body = viewProjects(m.merged, innerW, m.projectsVisible(), m.scrollProjects)
	case tabModels:
		body = viewModels(m.merged, innerW, m.pricing)
	case tabHosts:
		body = viewHosts(m.merged, innerW)
	case tabSessions:
		body = viewSessions(m.merged, innerW, m.pricing)
	case tabHeatmap:
		body = viewHeatmap(m.merged, innerW)
	case tabHourly:
		body = viewHourly(m.merged, innerW)
	}

	filterPart := ""
	if m.machineFilter != "" {
		filterPart = "  ·  " + warnStyle.Render("filter "+m.machineFilter)
	}
	header := titleStyle.Render("clawtop") +
		dimStyle.Render("  ·  "+time.Now().Format("15:04:05")) +
		filterPart +
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
	right := "tab/1-7 · t dash · f filter · r · q"
	used := lipgloss.Width(left) + lipgloss.Width(staleness) + lipgloss.Width(right)
	gap := w - used
	if gap < 1 {
		gap = 1
	}
	return dimStyle.Render(left) + staleness + strings.Repeat(" ", gap) + dimStyle.Render(right)
}

func (m model) viewLimits(w int) string {
	merged := m.merged
	sessR, weekR := m.burnRate()
	barW := w - 12
	sessDelta, weekDelta := "", ""
	if merged.HasHistory {
		sessDelta = "   " + dayDeltaLabel(merged.Session)
		weekDelta = "   " + dayDeltaLabel(merged.Week)
	}
	return strings.Join([]string{
		labelStyle.Render("SESSION  (5h)"),
		barRanked(merged.Session.Pct, barW) + "  " + pct(merged.Session.Pct) + sessDelta,
		dimStyle.Render("resets in " + short(merged.Session.ResetIn()) +
			projectionLine(merged.Session.Pct, sessR, merged.Session.ResetIn())),
		"",
		labelStyle.Render("WEEK     (7d)"),
		barRanked(merged.Week.Pct, barW) + "  " + pct(merged.Week.Pct) + weekDelta,
		dimStyle.Render("resets in " + short(merged.Week.ResetIn()) +
			projectionLine(merged.Week.Pct, weekR, merged.Week.ResetIn())),
		"",
		dimStyle.Render("plan: " + fallback(merged.Subscription, "?") + "    limit: " + fallback(merged.Limit, "?")),
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
	nameW := 26
	numW := 10
	touchW := 10
	barW := w - nameW - numW - touchW - 4
	if barW < 10 {
		barW = 10
	}
	now := time.Now()
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
		touched := "—"
		if p.LastAt > 0 {
			touched = short(now.Sub(time.Unix(p.LastAt, 0))) + " ago"
		}
		rows = append(rows, fmt.Sprintf("%-*s %s  %s  %s  %s",
			nameW, truncate(p.Name, nameW),
			barNeutral(ratio, barW),
			pad(fmtTokens(p.Total()), numW),
			trendLabel(p.Total(), p.PrevTotal()),
			dimStyle.Render(pad(touched, touchW)),
		))
		sub := ""
		if hosts, ok := m.HostsByProject[p.Path]; ok {
			sub = hostsLine(hosts, w-2)
		} else if p.Sessions > 0 {
			sub = fmt.Sprintf("%d sessions", p.Sessions)
		}
		if len(p.Branches) > 0 {
			brs := []string{}
			for _, b := range p.Branches {
				if b.Branch == "" {
					continue
				}
				brs = append(brs, fmt.Sprintf("%s %s", b.Branch, fmtTokens(b.Total())))
			}
			if len(brs) > 0 {
				if sub != "" {
					sub += " · "
				}
				sub += strings.Join(brs, " · ")
			}
		}
		if sub != "" {
			rows = append(rows, dimStyle.Render("  "+sub))
		}
	}
	if scroll > 0 || end < len(m.ByProject) {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("showing %d-%d of %d · j/k g G PgUp/PgDn",
			scroll+1, end, len(m.ByProject))))
	}
	return strings.Join(rows, "\n")
}

func viewModels(m merger.Merged, w int, pricing pricingTable) string {
	if len(m.ByModel) == 0 {
		return dimStyle.Render("no transcript data yet")
	}
	header := labelStyle.Render(fmt.Sprintf("MODELS (last %s)", fallback(m.Window, "?")))
	if total := pricing.totalCost(m.ByModel); total > 0 {
		suffix := "   est. " + fmtUSD(total)
		if mo, ok := monthlyCost(total, m.Window); ok {
			suffix += " · ~" + fmtUSD(mo) + "/mo"
		}
		header += dimStyle.Render(suffix + " (list price, validate against billing)")
	}
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
		saved := int64(float64(mm.CacheR) * 0.9)
		rows = append(rows,
			fmt.Sprintf("%-*s %s  %s  %s",
				nameW, truncate(prettyModel(mm.Model), nameW),
				barNeutral(ratio, barW),
				pad(fmtTokens(mm.Total()), numW),
				trendLabel(mm.Total(), mm.PrevTotal()),
			),
			dimStyle.Render(fmt.Sprintf("%-*s  in=%s  out=%s  cache_read=%s  cache_create=%s",
				nameW, "",
				fmtTokens(mm.In), fmtTokens(mm.Out),
				fmtTokens(mm.CacheR), fmtTokens(mm.CacheC))),
			dimStyle.Render(fmt.Sprintf("%-*s  cache_hit=%.0f%% (saved ~%s)  sessions=%d%s",
				nameW, "", mm.CacheHitRate(), fmtTokens(saved), mm.Sessions, modelCostSuffix(pricing, mm))),
		)
		if mm.WebSearch > 0 || mm.WebFetch > 0 {
			rows = append(rows, dimStyle.Render(fmt.Sprintf(
				"%-*s  web_search=%d  web_fetch=%d",
				nameW, "", mm.WebSearch, mm.WebFetch)))
		}
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

// viewHeatmap renders a 7x24 grid (day-of-week × hour-of-day) with cell
// intensity proportional to tokens. Each cell is a 2-char wide unicode
// block painted in cyan→magenta as intensity climbs.
func viewHeatmap(m merger.Merged, w int) string {
	header := labelStyle.Render("HEATMAP (last 7d · day × hour, all hosts)")
	var peak int64
	var total int64
	var peakDay, peakHour int
	for i, v := range m.Heatmap {
		total += v
		if v > peak {
			peak = v
			peakDay = i / 24
			peakHour = i % 24
		}
	}
	if peak == 0 {
		return strings.Join([]string{header, "", dimStyle.Render("no transcript data yet")}, "\n")
	}

	days := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}

	// Hour header row: print every 2 hours to keep width manageable.
	var hdr strings.Builder
	hdr.WriteString("     ")
	for h := 0; h < 24; h += 2 {
		hdr.WriteString(fmt.Sprintf("%-4d", h))
	}

	rows := []string{header, "", dimStyle.Render(hdr.String())}
	for d := 0; d < 7; d++ {
		var line strings.Builder
		line.WriteString(labelStyle.Render(days[d]) + "  ")
		for h := 0; h < 24; h++ {
			v := m.Heatmap[d*24+h]
			cell := heatCell(v, peak)
			line.WriteString(cell)
		}
		rows = append(rows, line.String())
	}
	rows = append(rows, "")
	rows = append(rows, dimStyle.Render(fmt.Sprintf(
		"total %s · peak %s on %s %02dh",
		fmtTokens(total), fmtTokens(peak), days[peakDay], peakHour)))
	return strings.Join(rows, "\n")
}

func heatCell(v, peak int64) string {
	if v == 0 || peak == 0 {
		return dimStyle.Render("░░")
	}
	ratio := float64(v) / float64(peak)
	var ch, color string
	switch {
	case ratio >= 0.75:
		ch = "██"
		color = "198" // magenta
	case ratio >= 0.50:
		ch = "▓▓"
		color = "205"
	case ratio >= 0.25:
		ch = "▒▒"
		color = "39" // cyan
	default:
		ch = "░░"
		color = "245"
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(ch)
}

// trendLabel returns a colored "↑+23%" / "↓-15%" / "·" label.
func trendLabel(curr, prev int64) string {
	if prev == 0 {
		return dimStyle.Render(" new ")
	}
	pct := domain.TrendPct(curr, prev)
	switch {
	case pct > 5:
		return okStyle.Render(fmt.Sprintf("↑%2.0f%%", pct))
	case pct < -5:
		return errStyle.Render(fmt.Sprintf("↓%2.0f%%", -pct))
	default:
		return dimStyle.Render("  ·  ")
	}
}

// viewSessions renders the top N most expensive sessions in the window. Each
// session takes two lines: a live-dot + title headline, then a dim meta line
// with project, model, tokens, cache hit rate, action breakdown, duration and
// last-seen. Useful to spot what each runaway conversation was actually doing.
func viewSessions(m merger.Merged, w int, pricing pricingTable) string {
	header := labelStyle.Render(fmt.Sprintf("TOP SESSIONS (last %s)", fallback(m.Window, "?")))
	if a := actionsSummary(m); a != "" {
		header += dimStyle.Render(" · " + a)
	}
	if len(m.TopSessions) == 0 {
		return strings.Join([]string{header, "", dimStyle.Render("no transcript data yet")}, "\n")
	}
	rows := []string{header, "", dimStyle.Render("● live (touched <5m)   actions: e=edits f=files r=reads b=bash   ⚠ tokens but no output"), ""}
	now := time.Now()
	for _, s := range m.TopSessions {
		dur := time.Duration(s.LastAt-s.StartedAt) * time.Second
		age := now.Sub(time.Unix(s.LastAt, 0))
		label := s.Title
		if label == "" {
			label = s.Project
		}
		title := truncate(label, w-2)
		if isSpinning(s) {
			title += " " + warnStyle.Render("⚠")
		}
		meta := []string{
			truncate(s.Project, 20),
			truncate(prettyModel(s.Model), 16),
			fmtTokens(s.Total()),
		}
		if c, ok := pricing.sessionCost(s); ok {
			meta = append(meta, fmtUSD(c))
		}
		if s.In+s.CacheR > 0 {
			meta = append(meta, fmt.Sprintf("cache %.0f%%", s.CacheHitRate()))
		}
		if act := sessionActions(s); act != "" {
			meta = append(meta, act)
		}
		meta = append(meta, short(dur), short(age)+" ago")
		rows = append(rows,
			fmt.Sprintf("%s %s", liveDot(s.LastAt), title),
			dimStyle.Render("  "+truncate(strings.Join(meta, " · "), w-2)),
		)
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
		dimStyle.Render(strings.Repeat("▒", rest))
}

// barNeutral is a single-color bar suitable for relative comparisons (project
// rank, model split) where the value doesn't carry good/bad semantics.
func barNeutral(pct float64, width int) string {
	filled, rest := barCells(pct, width)
	return neutralBar.Render(strings.Repeat("█", filled)) +
		dimStyle.Render(strings.Repeat("▒", rest))
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

// dayDeltaLabel renders the percentage-point change of a rate-limit window vs
// ~24h ago. Rising utilization is bad (warn), falling is good (ok).
func dayDeltaLabel(w domain.Window) string {
	d := w.DayDelta()
	switch {
	case d > 0.05:
		return warnStyle.Render(fmt.Sprintf("↑%.1fpp/24h", d))
	case d < -0.05:
		return okStyle.Render(fmt.Sprintf("↓%.1fpp/24h", -d))
	default:
		return dimStyle.Render("≈flat/24h")
	}
}

// windowDuration parses the daemon's window label ("24h", "7d", "30d", or a
// Go duration string) into a duration. Zero when unparseable.
func windowDuration(s string) time.Duration {
	if s == "" {
		return 0
	}
	if n, err := strconv.Atoi(strings.TrimSuffix(s, "d")); err == nil && strings.HasSuffix(s, "d") {
		return time.Duration(n) * 24 * time.Hour
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return 0
}

// monthlyCost extrapolates the window's total cost to a 30-day run rate. ok is
// false when the window is unknown or there's no cost yet.
func monthlyCost(total float64, window string) (float64, bool) {
	d := windowDuration(window)
	if d <= 0 || total <= 0 {
		return 0, false
	}
	month := 30 * 24 * time.Hour
	return total * float64(month) / float64(d), true
}

// liveCount returns how many of the top sessions were touched in the last 5
// minutes. Note: scoped to the top sessions list, not every session.
func liveCount(m merger.Merged) int {
	n := 0
	for _, s := range m.TopSessions {
		if s.LastAt > 0 && time.Since(time.Unix(s.LastAt, 0)) < 5*time.Minute {
			n++
		}
	}
	return n
}

// liveDot returns a green ● for sessions touched within the last 5 minutes
// (still active), else a dim ○ (idle/done).
func liveDot(lastAt int64) string {
	if lastAt > 0 && time.Since(time.Unix(lastAt, 0)) < 5*time.Minute {
		return okStyle.Render("●")
	}
	return dimStyle.Render("○")
}

// actionsSummary renders account-level edits/reads/bash compactly. Empty when
// all are zero (e.g. an older daemon that didn't report them).
func actionsSummary(m merger.Merged) string {
	if m.Edits == 0 && m.Reads == 0 && m.Bash == 0 {
		return ""
	}
	return fmt.Sprintf("%s edits · %s reads · %s bash",
		fmtTokens(m.Edits), fmtTokens(m.Reads), fmtTokens(m.Bash))
}

// modelCostSuffix renders "  est=$X" for a model when its family is priced,
// else empty.
func modelCostSuffix(pricing pricingTable, mm domain.Model) string {
	if c, ok := pricing.modelCost(mm); ok {
		return "  est=" + fmtUSD(c)
	}
	return ""
}

// spinThreshold is the token count above which a session that produced no
// concrete output is considered to be "spinning" (stuck exploring/discussing
// without acting). Picked to ignore small read-only Q&A sessions.
const spinThreshold = 200_000

// isSpinning flags a session that burned significant tokens but made no edits,
// touched no files, and ran no shell commands — usually the model stuck in a
// loop rather than doing work.
func isSpinning(s domain.SessionStat) bool {
	return s.Total() >= spinThreshold && s.Edits == 0 && s.FilesTouched == 0 && s.Bash == 0
}

// sessionActions renders a per-session action breakdown like "12e 3f 40r 8b"
// (edits, files touched, reads, bash). Empty when the session did nothing.
func sessionActions(s domain.SessionStat) string {
	parts := make([]string, 0, 4)
	if s.Edits > 0 {
		parts = append(parts, fmt.Sprintf("%de", s.Edits))
	}
	if s.FilesTouched > 0 {
		parts = append(parts, fmt.Sprintf("%df", s.FilesTouched))
	}
	if s.Reads > 0 {
		parts = append(parts, fmt.Sprintf("%dr", s.Reads))
	}
	if s.Bash > 0 {
		parts = append(parts, fmt.Sprintf("%db", s.Bash))
	}
	return strings.Join(parts, " ")
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
