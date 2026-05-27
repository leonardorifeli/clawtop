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
	"github.com/leonardorifeli/clawtop/internal/service/collector"
)

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
