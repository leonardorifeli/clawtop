package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"time"

	"github.com/leonardorifeli/clawtop/external/anthropic"
	"github.com/leonardorifeli/clawtop/external/push"
)

// runDoctor performs preflight checks and returns the exit code.
// It uses the same flag set as runDaemon for consistency.
func runDoctor(args []string) int {
	cfg := parseRunFlags(args)

	fmt.Println("clawtopd doctor")
	fmt.Println("===============")

	ok := true

	// 1. Credentials.
	creds, err := anthropic.LoadCreds(cfg.credsPath)
	if err != nil {
		report("FAIL", "credentials", cfg.credsPath, err.Error())
		report("HINT", "credentials", "", "run 'claude' once to create the credentials file")
		ok = false
	} else {
		expires := "never"
		if creds.ExpiresAt > 0 {
			expires = time.UnixMilli(creds.ExpiresAt).Format(time.RFC3339)
		}
		report("OK  ", "credentials", cfg.credsPath, fmt.Sprintf("subscription=%s expires=%s", creds.SubscriptionType, expires))
	}

	// 2. Anthropic probe (skippable).
	if cfg.skipProbe {
		report("SKIP", "anthropic", "", "--skip-probe set")
	} else if creds != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		client := anthropic.New(cfg.credsPath)
		s, err := client.Probe(ctx)
		cancel()
		if err != nil {
			report("FAIL", "anthropic", "api.anthropic.com", err.Error())
			ok = false
		} else {
			report("OK  ", "anthropic", "api.anthropic.com",
				fmt.Sprintf("session=%.1f%% week=%.1f%% limit=%s", s.Session.Pct, s.Week.Pct, s.Limit))
		}
	} else {
		report("SKIP", "anthropic", "", "credentials check failed; cannot probe")
	}

	// 3. Destination.
	pusher := newPusher(cfg)
	target := path.Join(cfg.dir, cfg.machineID+".json")
	switch cfg.host {
	case "localhost":
		if err := testLocalWrite(pusher); err != nil {
			report("FAIL", "destination", "local:"+target, err.Error())
			ok = false
		} else {
			report("OK  ", "destination", "local:"+target, "writable")
		}
	default:
		ssh, _ := pusher.(push.SSH)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := ssh.Reachable(ctx)
		cancel()
		if err != nil {
			report("FAIL", "ssh", cfg.host, err.Error())
			report("HINT", "ssh", "", "verify with: ssh "+cfg.host+" true")
			report("HINT", "ssh", "", "first-time setup: ssh-copy-id "+cfg.host)
			ok = false
		} else {
			report("OK  ", "ssh", cfg.host, "key auth works")
			if err := testRemoteWrite(ssh); err != nil {
				report("FAIL", "destination", "ssh:"+cfg.host+":"+target, err.Error())
				ok = false
			} else {
				report("OK  ", "destination", "ssh:"+cfg.host+":"+target, "writable")
			}
		}
	}

	fmt.Println()
	if !ok {
		fmt.Fprintln(os.Stderr, "doctor: one or more checks failed")
		return 1
	}
	fmt.Println("doctor: all clear")
	return 0
}

func report(status, name, where, detail string) {
	if where != "" {
		fmt.Printf("%s  %-13s  %s  %s\n", status, name, where, dim(detail))
	} else {
		fmt.Printf("%s  %-13s  %s\n", status, name, dim(detail))
	}
}

// dim wraps a string in ANSI faint, but only if stdout looks like a TTY.
// We avoid the github.com/mattn/go-isatty dep — the env var heuristic is
// good enough for our use.
func dim(s string) string {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return s
	}
	if !isTerminal() {
		return s
	}
	return "\033[2m" + s + "\033[0m"
}

func isTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

func testLocalWrite(p push.Pusher) error {
	local, ok := p.(push.Local)
	if !ok {
		return fmt.Errorf("not a local pusher")
	}
	probe := push.Local{Path: local.Path + ".doctor"}
	if err := probe.Push(context.Background(), []byte("{}")); err != nil {
		return err
	}
	return os.Remove(probe.Path)
}

func testRemoteWrite(s push.SSH) error {
	probe := push.SSH{Host: s.Host, Path: s.Path + ".doctor"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := probe.Push(ctx, []byte("{}")); err != nil {
		return err
	}
	// Best-effort cleanup; ignore errors. The path was constructed from a
	// user-controlled --dir + machine ID, so quote it for the remote shell.
	rm := exec.CommandContext(ctx, "ssh",
		"-o", "BatchMode=yes", "-o", "ConnectTimeout=10",
		s.Host, "rm -f '"+probe.Path+"'")
	_ = rm.Run()
	return nil
}
