// Package alert turns rate-limit observations into notifications. It is split
// into a pure evaluation step (which alerts are firing given the current
// windows and config) and a stateful Notifier that edge-triggers delivery so a
// crossed threshold notifies once, not every poll.
package alert

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"time"
)

// Config controls which conditions raise an alert. A zero threshold disables
// that window's threshold check.
type Config struct {
	SessionPct float64 // alert when the 5h window reaches this percent (0 = off)
	WeekPct    float64 // alert when the 7d window reaches this percent (0 = off)
	Project    bool    // alert when burn rate projects hitting 100% before reset
}

// Enabled reports whether any alert condition is configured.
func (c Config) Enabled() bool {
	return c.SessionPct > 0 || c.WeekPct > 0 || c.Project
}

// Window is one rate-limit window's current state at poll time.
type Window struct {
	Name    string        // "session" or "week"
	Pct     float64       // current utilization 0..100
	ResetIn time.Duration // time until the window resets (0 = unknown)
	Rate    float64       // burn rate in percent-per-hour (0 = unknown)
}

// Alert is one firing condition. Key is stable across polls so the Notifier can
// edge-trigger; Level is "warning" or "urgent".
type Alert struct {
	Key     string
	Title   string
	Message string
	Level   string
}

// Evaluate returns the alerts currently firing for the given windows under cfg.
// Pure: same inputs always yield the same output.
func Evaluate(cfg Config, windows []Window) []Alert {
	var out []Alert
	for _, w := range windows {
		threshold := cfg.SessionPct
		if w.Name == "week" {
			threshold = cfg.WeekPct
		}
		if threshold > 0 && w.Pct >= threshold {
			level := "warning"
			if w.Pct >= 95 {
				level = "urgent"
			}
			out = append(out, Alert{
				Key:     w.Name + ":threshold",
				Title:   fmt.Sprintf("clawtop: %s at %.0f%%", w.Name, w.Pct),
				Message: fmt.Sprintf("%s window is at %.1f%% (threshold %.0f%%), resets in %s", w.Name, w.Pct, threshold, shortDur(w.ResetIn)),
				Level:   level,
			})
		}
		if cfg.Project && w.Rate > 0 && w.Pct < 100 && w.ResetIn > 0 {
			hoursToCap := (100 - w.Pct) / w.Rate
			toCap := time.Duration(hoursToCap * float64(time.Hour))
			if toCap < w.ResetIn {
				out = append(out, Alert{
					Key:     w.Name + ":projection",
					Title:   fmt.Sprintf("clawtop: %s projected over", w.Name),
					Message: fmt.Sprintf("%s at %.1f%% burning %.1f%%/h — projected to hit 100%% in %s, before reset in %s", w.Name, w.Pct, w.Rate, shortDur(toCap), shortDur(w.ResetIn)),
					Level:   "urgent",
				})
			}
		}
	}
	return out
}

// Notifier edge-triggers delivery: an alert fires once when it starts, and
// re-arms only after it stops firing (e.g. the window reset or burn slowed).
type Notifier struct {
	cfg    Config
	firing map[string]bool
	emit   func(Alert) // delivery sink, swappable in tests
}

// New builds a Notifier that logs every alert via logf and, when set, also
// POSTs to url (ntfy/webhook) and runs cmd (via "sh -c").
func New(cfg Config, url, cmd string, logf func(string, ...any)) *Notifier {
	return &Notifier{
		cfg:    cfg,
		firing: map[string]bool{},
		emit:   func(a Alert) { deliver(a, url, cmd, logf) },
	}
}

// Process evaluates the windows and delivers any newly-firing alerts.
func (n *Notifier) Process(windows []Window) {
	active := Evaluate(n.cfg, windows)
	now := map[string]bool{}
	for _, a := range active {
		now[a.Key] = true
		if !n.firing[a.Key] {
			n.emit(a) // edge: transitioned into firing
		}
	}
	n.firing = now // anything absent re-arms for next time
}

// deliver fans an alert out to the log, an optional HTTP endpoint, and an
// optional shell command. Delivery failures are logged, never fatal.
func deliver(a Alert, url, cmd string, logf func(string, ...any)) {
	if logf != nil {
		logf("ALERT [%s] %s", a.Level, a.Message)
	}
	if url != "" {
		if err := postNotify(url, a); err != nil && logf != nil {
			logf("alert: POST %s failed: %v", url, err)
		}
	}
	if cmd != "" {
		if err := runNotify(cmd, a); err != nil && logf != nil {
			logf("alert: cmd failed: %v", err)
		}
	}
}

// postNotify sends the alert as a text/plain body with ntfy-style Title and
// Priority headers, which also works for generic webhooks that read the body.
func postNotify(url string, a Alert) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewBufferString(a.Message))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Title", a.Title)
	priority := "high"
	if a.Level == "urgent" {
		priority = "max"
	}
	req.Header.Set("Priority", priority)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// runNotify runs cmd via "sh -c", exposing the alert fields as environment
// variables so a hook (notify-send, a Slack script, etc.) can format freely.
func runNotify(cmd string, a Alert) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	c.Env = append(os.Environ(),
		"CLAWTOP_ALERT_TITLE="+a.Title,
		"CLAWTOP_ALERT_MESSAGE="+a.Message,
		"CLAWTOP_ALERT_LEVEL="+a.Level,
		"CLAWTOP_ALERT_KEY="+a.Key,
	)
	return c.Run()
}

func shortDur(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}
