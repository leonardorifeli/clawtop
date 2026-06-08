// clawtopd polls Anthropic for rate-limit headers, aggregates local Claude
// Code transcripts, and writes a per-machine status JSON. When --host is
// "localhost" (default) it writes to the local filesystem; otherwise it
// pushes to the remote host via SSH.
//
// Subcommands:
//
//	clawtopd               run the daemon (default)
//	clawtopd doctor        preflight check: credentials, Anthropic, destination
//	clawtopd help          print usage
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/leonardorifeli/clawtop/external/anthropic"
	"github.com/leonardorifeli/clawtop/external/push"
	"github.com/leonardorifeli/clawtop/internal/domain"
	"github.com/leonardorifeli/clawtop/internal/service/alert"
	"github.com/leonardorifeli/clawtop/internal/service/collector"
	"github.com/leonardorifeli/clawtop/internal/service/history"
)

// limitSample records one observation of the rate-limit windows, for deriving
// a burn rate to feed the alert projection.
type limitSample struct {
	at   time.Time
	sess float64
	week float64
}

// burnRate returns percent-per-hour for session and week from the sample
// history. Returns 0,0 until there are two distinct-time samples; a window
// reset (negative delta) is floored to 0 so it never reads as negative burn.
func burnRate(h []limitSample) (sessRate, weekRate float64) {
	if len(h) < 2 {
		return 0, 0
	}
	first, last := h[0], h[len(h)-1]
	dt := last.at.Sub(first.at).Hours()
	if dt <= 0 {
		return 0, 0
	}
	ds, dw := last.sess-first.sess, last.week-first.week
	if ds < 0 {
		ds = 0
	}
	if dw < 0 {
		dw = 0
	}
	return ds / dt, dw / dt
}

var safeID = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

	args := os.Args[1:]
	cmd := ""
	if len(args) > 0 {
		switch args[0] {
		case "doctor", "run", "help", "-h", "--help":
			cmd = args[0]
			args = args[1:]
		}
	}

	switch cmd {
	case "doctor":
		os.Exit(runDoctor(args))
	case "help", "-h", "--help":
		printUsage()
	default:
		runDaemon(args)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `clawtopd — push Claude usage to a viewer

Usage:
  clawtopd [flags]            run the daemon
  clawtopd doctor [flags]     preflight check
  clawtopd help               this help

Run 'clawtopd -h' or 'clawtopd doctor -h' for flag details.`)
}

type runConfig struct {
	credsPath    string
	host         string
	dir          string
	machineID    string
	projectsRoot string
	window       time.Duration
	interval     time.Duration
	once         bool
	localOnly    bool
	skipProbe    bool
	alertSession float64
	alertWeek    float64
	alertProject bool
	notifyURL    string
	notifyCmd    string
	historyDir   string
	historyKeep  time.Duration
}

func parseRunFlags(args []string) *runConfig {
	fs := flag.NewFlagSet("clawtopd", flag.ExitOnError)
	defaultCreds := filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")
	host, _ := os.Hostname()

	cfg := &runConfig{}
	fs.StringVar(&cfg.credsPath, "creds", defaultCreds, "path to Claude OAuth credentials JSON")
	fs.StringVar(&cfg.host, "host", "localhost", `destination: "localhost" writes locally, anything else is treated as an ssh_config alias and the payload is pushed via SSH`)
	fs.StringVar(&cfg.dir, "dir", "", "directory for the status JSON (default: ~/.local/share/clawtop for localhost, /var/lib/clawtop for SSH)")
	fs.StringVar(&cfg.machineID, "machine", safeID.ReplaceAllString(host, "-"), "stable identifier for this machine (used as filename)")
	fs.StringVar(&cfg.projectsRoot, "projects", collector.DefaultRoot(), "Claude Code transcripts directory")
	fs.DurationVar(&cfg.window, "window", 7*24*time.Hour, "aggregation lookback window")
	fs.DurationVar(&cfg.interval, "interval", 60*time.Second, "poll interval")
	fs.BoolVar(&cfg.once, "once", false, "probe once and exit (useful for testing)")
	fs.BoolVar(&cfg.localOnly, "local-only", false, "do not push; print status JSON to stdout")
	fs.BoolVar(&cfg.skipProbe, "skip-probe", false, "do not call Anthropic; aggregate transcripts only")
	fs.Float64Var(&cfg.alertSession, "alert-session", 0, "alert when the 5h session window reaches this percent (0 = off)")
	fs.Float64Var(&cfg.alertWeek, "alert-week", 0, "alert when the 7d week window reaches this percent (0 = off)")
	fs.BoolVar(&cfg.alertProject, "alert-project", false, "alert when burn rate projects hitting 100% before the window resets")
	fs.StringVar(&cfg.notifyURL, "notify-url", "", "POST alerts to this URL (ntfy topic or generic webhook)")
	fs.StringVar(&cfg.notifyCmd, "notify-cmd", "", "run this command (via sh -c) per alert; details in $CLAWTOP_ALERT_* env vars")
	defaultHistory := filepath.Join(os.Getenv("HOME"), ".local", "share", "clawtop")
	fs.StringVar(&cfg.historyDir, "history-dir", defaultHistory, "local directory for rate-limit history (empty = disabled); used to report day-over-day deltas")
	fs.DurationVar(&cfg.historyKeep, "history-keep", 30*24*time.Hour, "how long to retain history samples")
	_ = fs.Parse(args)

	if cfg.dir == "" {
		if cfg.host == "localhost" {
			cfg.dir = filepath.Join(os.Getenv("HOME"), ".local", "share", "clawtop")
		} else {
			cfg.dir = "/var/lib/clawtop"
		}
	}
	cfg.dir = expandHome(cfg.dir)
	cfg.credsPath = expandHome(cfg.credsPath)
	cfg.projectsRoot = expandHome(cfg.projectsRoot)
	cfg.historyDir = expandHome(cfg.historyDir)
	if cfg.machineID == "" {
		log.Fatal("--machine required when hostname is empty")
	}
	return cfg
}

// expandHome resolves a leading "~" or "~/" to the current user's home
// directory. Shells do not tilde-expand the value side of --flag=value
// arguments, so users writing `--dir=~/foo` would otherwise get a literal
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

// newPusher returns the appropriate Pusher for the configured host.
// "localhost" → local file; any other value → SSH alias.
func newPusher(cfg *runConfig) push.Pusher {
	target := path.Join(cfg.dir, cfg.machineID+".json")
	if cfg.host == "localhost" {
		return push.Local{Path: target}
	}
	return push.SSH{Host: cfg.host, Path: target}
}

func runDaemon(args []string) {
	cfg := parseRunFlags(args)
	client := anthropic.New(cfg.credsPath)
	pusher := newPusher(cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	// Last successful probe is held across polls so a transient Anthropic
	// failure (rate-limit, network blip) does not blank the dashboard.
	// Transcript aggregation always runs from fresh, since it is local I/O.
	var lastProbed *domain.Status

	// Alerting: derive a burn rate from a small rolling sample history and
	// edge-trigger notifications. Nil notifier when no alert flag is set.
	var samples []limitSample
	var notifier *alert.Notifier
	alertCfg := alert.Config{SessionPct: cfg.alertSession, WeekPct: cfg.alertWeek, Project: cfg.alertProject}
	if alertCfg.Enabled() {
		notifier = alert.New(alertCfg, cfg.notifyURL, cfg.notifyCmd, log.Printf)
	}

	// Rate-limit history: local state used to report day-over-day deltas.
	// Prune once at startup so it stays bounded across restarts.
	hist, err := history.New(cfg.historyDir, cfg.machineID)
	if err != nil {
		log.Printf("history: %v (disabled)", err)
	}
	if hist != nil {
		if err := hist.Prune(time.Now(), cfg.historyKeep); err != nil {
			log.Printf("history prune: %v", err)
		}
	}

	run := func() {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		s := &domain.Status{
			Schema:  domain.Version,
			Machine: cfg.machineID,
			TS:      time.Now().Unix(),
			Window:  shortWindow(cfg.window),
		}

		if !cfg.skipProbe {
			probed, err := client.Probe(probeCtx)
			if err != nil {
				log.Printf("probe: %v (keeping last-known-good)", err)
				if lastProbed != nil {
					s.Session = lastProbed.Session
					s.Week = lastProbed.Week
					s.Limit = lastProbed.Limit
					s.Subscription = lastProbed.Subscription
				}
			} else {
				s.Session = probed.Session
				s.Week = probed.Week
				s.Limit = probed.Limit
				s.Subscription = probed.Subscription
				lastProbed = probed
			}
		}

		// Evaluate alerts from the rate-limit windows (independent of the
		// transcript aggregation below). Skip when both pcts are zero — a
		// failed first probe with no last-known-good has nothing to project.
		if notifier != nil && (s.Session.Pct > 0 || s.Week.Pct > 0) {
			samples = append(samples, limitSample{at: time.Now(), sess: s.Session.Pct, week: s.Week.Pct})
			if len(samples) > 30 {
				samples = samples[len(samples)-30:]
			}
			sr, wr := burnRate(samples)
			notifier.Process([]alert.Window{
				{Name: "session", Pct: s.Session.Pct, ResetIn: s.Session.ResetIn(), Rate: sr},
				{Name: "week", Pct: s.Week.Pct, ResetIn: s.Week.ResetIn(), Rate: wr},
			})
		}

		// Record history and report the ~24h-ago values as deltas in the
		// payload, so viewers show day-over-day trend without reading the file.
		if hist != nil && (s.Session.Pct > 0 || s.Week.Pct > 0) {
			now := time.Now()
			if err := hist.Append(now.Unix(), s.Session.Pct, s.Week.Pct); err != nil {
				log.Printf("history append: %v", err)
			}
			if prev, ok := hist.At(now, 24*time.Hour); ok {
				s.Session.PrevDayPct = prev.Sess
				s.Week.PrevDayPct = prev.Week
				s.HasHistory = true
			}
		}

		agg, err := collector.Collect(collector.Options{
			Root:   cfg.projectsRoot,
			Window: cfg.window,
		})
		if err != nil {
			log.Printf("collect: %v", err)
		} else {
			s.ByProject = capProjects(agg.ByProject, 20)
			s.ByModel = agg.ByModel
			s.Hourly24h = agg.Hourly24h
			s.Daily7d = agg.Daily7d
			s.Sessions = agg.Sessions
			s.TopSessions = agg.TopSessions
			s.Heatmap = agg.Heatmap
			s.WebSearch = agg.WebSearch
			s.WebFetch = agg.WebFetch
			s.Edits = agg.Edits
			s.Reads = agg.Reads
			s.Bash = agg.Bash
		}

		payload, _ := json.Marshal(s)
		if cfg.localOnly {
			os.Stdout.Write(payload)
			os.Stdout.WriteString("\n")
			return
		}
		if err := pusher.Push(probeCtx, payload); err != nil {
			log.Printf("push: %v", err)
			return
		}
		log.Printf("ok machine=%s session=%.1f%% week=%.1f%% projects=%d models=%d via=%s",
			cfg.machineID, s.Session.Pct, s.Week.Pct, len(s.ByProject), len(s.ByModel), pusher.Describe())
	}

	run()
	if cfg.once {
		return
	}

	t := time.NewTicker(cfg.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// capProjects keeps only the top-N projects to bound payload size.
func capProjects(ps []domain.Project, n int) []domain.Project {
	if len(ps) <= n {
		return ps
	}
	return ps[:n]
}

// shortWindow renders a duration as "24h", "7d", "30d" for the payload.
func shortWindow(d time.Duration) string {
	if d%(24*time.Hour) == 0 {
		days := int(d / (24 * time.Hour))
		if days == 1 {
			return "24h"
		}
		return fmt.Sprintf("%dd", days)
	}
	return d.String()
}
