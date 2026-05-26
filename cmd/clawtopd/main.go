// clawtopd polls Anthropic for rate-limit headers, aggregates local Claude
// Code transcripts, and pushes a per-machine status JSON to a remote host
// via SSH. Runs on the same machine that holds the Claude OAuth credentials;
// the credentials never leave this host.
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
	"syscall"
	"time"

	"github.com/leonardorifeli/clawtop/external/anthropic"
	"github.com/leonardorifeli/clawtop/external/push"
	"github.com/leonardorifeli/clawtop/internal/domain"
	"github.com/leonardorifeli/clawtop/internal/service/collector"
)

var safeID = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

func main() {
	defaultCreds := filepath.Join(os.Getenv("HOME"), ".claude", ".credentials.json")
	host, _ := os.Hostname()

	var (
		credsPath    = flag.String("creds", defaultCreds, "path to Claude OAuth credentials JSON")
		remoteHost   = flag.String("host", "localhost", "ssh_config alias of the viewer host (or 'localhost' to write locally)")
		remoteDir    = flag.String("remote-dir", "/var/lib/clawtop", "remote directory for per-machine status JSON files")
		machineID    = flag.String("machine", safeID.ReplaceAllString(host, "-"), "stable identifier for this machine (used as filename)")
		projectsRoot = flag.String("projects", collector.DefaultRoot(), "Claude Code transcripts directory")
		windowFlag   = flag.Duration("window", 7*24*time.Hour, "aggregation lookback window")
		interval     = flag.Duration("interval", 60*time.Second, "poll interval")
		once         = flag.Bool("once", false, "probe once and exit (useful for testing)")
		localOnly    = flag.Bool("local-only", false, "do not push; print status JSON to stdout")
		skipProbe    = flag.Bool("skip-probe", false, "do not call Anthropic; aggregate transcripts only")
	)
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.SetOutput(os.Stderr)

	if *machineID == "" {
		log.Fatal("--machine required when hostname is empty")
	}

	client := anthropic.New(*credsPath)
	pusher := push.SSH{
		Host: *remoteHost,
		Path: path.Join(*remoteDir, *machineID+".json"),
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	run := func() {
		probeCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()

		s := &domain.Status{
			Schema:  domain.Version,
			Machine: *machineID,
			TS:      time.Now().Unix(),
			Window:  shortWindow(*windowFlag),
		}

		if !*skipProbe {
			probed, err := client.Probe(probeCtx)
			if err != nil {
				log.Printf("probe: %v", err)
			} else {
				s.Session = probed.Session
				s.Week = probed.Week
				s.Limit = probed.Limit
				s.Subscription = probed.Subscription
			}
		}

		agg, err := collector.Collect(collector.Options{
			Root:   *projectsRoot,
			Window: *windowFlag,
		})
		if err != nil {
			log.Printf("collect: %v", err)
		} else {
			s.ByProject = capProjects(agg.ByProject, 20)
			s.ByModel = agg.ByModel
			s.Hourly24h = agg.Hourly24h
		}

		payload, _ := json.Marshal(s)
		if *localOnly {
			os.Stdout.Write(payload)
			os.Stdout.WriteString("\n")
			return
		}
		if err := pusher.Push(probeCtx, payload); err != nil {
			log.Printf("push: %v", err)
			return
		}
		log.Printf("ok machine=%s session=%.1f%% week=%.1f%% projects=%d models=%d",
			*machineID, s.Session.Pct, s.Week.Pct, len(s.ByProject), len(s.ByModel))
	}

	run()
	if *once {
		return
	}

	t := time.NewTicker(*interval)
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
