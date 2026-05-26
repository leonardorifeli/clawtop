# clawtop

A TUI dashboard for your Anthropic Claude usage. Always-on, multi-host
aware, and built so the OAuth credentials never leave the machine that
already holds them.

```
╭─ clawtop · 14:32:01   1 Limits · 2 Projects · 3 Models · 4 Hourly ─────╮
│                                                                         │
│  SESSION  (5h)                                                          │
│  ████████████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░     42.1%            │
│  resets in 2h 17m                                                       │
│                                                                         │
│  WEEK     (7d)                                                          │
│  ██████████░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░░     24.4%            │
│  resets in 4d 8h                                                        │
│                                                                         │
│  plan: max    limit: allowed                                            │
│                                                                         │
│  hosts: 3 (laptop,omen,workpc)  window: 7d  fresh                       │
│  tab/← → switch · 1-4 jump · r reload · q quit                          │
╰─────────────────────────────────────────────────────────────────────────╯
```

## Quickstart — single PC (most people)

You run Claude on one machine and want a dashboard on that same machine.

```bash
# 1. Install both binaries.
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh

# 2. Verify everything is wired correctly.
clawtopd doctor
# OK    credentials   ~/.claude/.credentials.json   subscription=max
# OK    anthropic     api.anthropic.com             session=12.1% week=4.3%
# OK    destination   local:~/.local/share/clawtop  writable

# 3. Run the daemon as a user service.
install -Dm644 /usr/lib/systemd/user/clawtopd-local.service ~/.config/systemd/user/clawtopd-local.service 2>/dev/null \
  || curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/deploy/clawtopd-local.service \
       -o ~/.config/systemd/user/clawtopd-local.service
systemctl --user daemon-reload
systemctl --user enable --now clawtopd-local

# 4. Open the dashboard whenever you want.
clawtop
```

No SSH, no remote host, no extra config. The daemon writes to
`~/.local/share/clawtop/<machine>.json` and the TUI reads from there.

## Quickstart — multi PC (N daemons → 1 viewer)

You run Claude on several machines (laptop, desktop, work box) and want
one dashboard that merges them. The OAuth credentials stay on each Claude
machine; only the derived JSON travels.

```bash
# --- On each machine that runs Claude ---
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh -s -- daemon
clawtopd doctor --host=<viewer-alias>       # verify SSH and remote write
# Edit deploy/clawtopd-remote.service to set --host=<viewer-alias>, then:
install -Dm644 clawtopd-remote.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now clawtopd-remote

# --- On the viewer host (any always-on box you can SSH into) ---
sudo install -d -o $USER -g $USER /var/lib/clawtop
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh -s -- viewer
install -Dm644 clawtop.service ~/.config/systemd/user/
systemctl --user enable --now clawtop
sudo loginctl enable-linger $USER           # keep the user service alive

# Then to look:
ssh <viewer-host>
tmux attach -t clawtop
```

The full multi-PC walkthrough (with SSH setup, troubleshooting, etc.) is
in [`deploy/INSTALL.md`](deploy/INSTALL.md).

## What it answers, in four tabs

1. **Limits** — how much of your 5-hour and 7-day rate-limit windows you
   are using, and when they reset.
2. **Projects** — which working directories ate your token budget over
   the chosen lookback window.
3. **Models** — Opus vs Sonnet vs Haiku split, with input, output, and
   cache-read/cache-create columns.
4. **Hourly** — a 24-hour sparkline of token consumption so you can spot
   the peak hours.

## What it isn't (and what to use instead)

clawtop is one of several open-source tools that read your local Claude
transcripts. Pick the shape that matches your use case:

| Tool | Shape | Best when |
|------|-------|-----------|
| [`ccusage`](https://github.com/ryoppippi/ccusage) | On-demand CLI, prints tables | You want a fast snapshot from the shell |
| [`Claude-Code-Usage-Monitor`](https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor) | Always-on terminal monitor with burn-rate predictions | You want ML projections on a single host |
| [`ccflare`](https://claudefa.st/blog/tools/monitors/claude-code-usage-monitor) | Browser dashboard | You want graphs you can pan and zoom |
| [`Clawdmeter`](https://github.com/HermannBjorgvin/Clawdmeter) | ESP32 hardware display | You want a physical Clawd on your desk |
| **`clawtop`** | TUI, single PC or **multi-host aware** | You run Claude on more than one machine, want one place to look, and don't want to copy credentials around |

If you only run Claude on a single laptop and just want a quick table,
`ccusage` is probably what you want.

## Architecture

**Single PC** — the daemon writes a local JSON, the TUI reads it:

```
┌─ your PC ──────────────────────────────────────────┐
│  ~/.claude/.credentials  ─▶ clawtopd  ─▶ ~/.local/share/clawtop/<machine>.json  ─▶ clawtop
│                              │
│                              ▼
│                          api.anthropic.com   (1 Haiku token / minute)
└─────────────────────────────────────────────────────┘
```

**Multi PC** — each daemon pushes its own JSON to a chosen viewer host:

```
┌──────────────┐                          ┌──────────────────┐
│ laptop       │──clawtopd──┐             │ viewer host      │
│              │            │             │                  │
│ ~/.claude/   │            │             │  /var/lib/       │
│ .credentials │            │   ssh push  │   clawtop/       │
└──────────────┘            ├────atomic──▶│   laptop.json    │
                            │             │   omen.json      │
┌──────────────┐            │             │   workpc.json    │
│ omen         │──clawtopd──┤             │        │         │
└──────────────┘            │             │        ▼         │
                            │             │  clawtop (TUI)   │
┌──────────────┐            │             │  inside tmux     │
│ workpc       │──clawtopd──┘             └──────────────────┘
└──────────────┘
        │
        │ HTTPS (1 Haiku token / minute / machine)
        ▼
   api.anthropic.com
```

Either way: the daemon polls Anthropic every 60 s with `max_tokens: 1` to
read the `anthropic-ratelimit-unified-*` headers, and walks
`~/.claude/projects/**/*.jsonl` to aggregate per-project, per-model, and
hourly token usage from your local transcripts.

## Credentials never travel

`clawtopd` runs as a systemd `--user` service, so it inherits the same
file permissions as your interactive shell. It reads
`~/.claude/.credentials.json` like any other file, then talks to
Anthropic from the same host. The viewer (whether on the same PC or on a
remote box) only sees the derived JSON — never the OAuth token. If a
remote viewer host is compromised, the attacker gets percentages and
token counts, not your account.

## Configuration

`clawtopd run` (default subcommand):

| flag | default | what |
|------|---------|------|
| `--host` | `localhost` | `localhost` writes locally; anything else is treated as an ssh_config alias and pushed via SSH |
| `--dir` | `~/.local/share/clawtop` (local) or `/var/lib/clawtop` (ssh) | directory for the per-machine status JSON |
| `--creds` | `~/.claude/.credentials.json` | Claude OAuth credentials file |
| `--machine` | hostname | stable identifier, becomes `<machine>.json` |
| `--projects` | `~/.claude/projects` | transcripts root (honors `$CLAUDE_CONFIG_DIR`) |
| `--window` | `168h` (7d) | aggregation lookback |
| `--interval` | `60s` | poll cadence |
| `--once` | `false` | probe once and exit (smoke test) |
| `--local-only` | `false` | print to stdout instead of writing |
| `--skip-probe` | `false` | skip the Anthropic call, aggregate transcripts only |

`clawtopd doctor` accepts the same flags and runs three preflight checks:
credentials are readable, Anthropic responds, destination is writable.

`clawtop`:

| flag | default | what |
|------|---------|------|
| `--dir` | `/var/lib/clawtop` | directory of per-machine status JSON files |

For single-PC use, point the TUI at the daemon's local dir:
`clawtop --dir=~/.local/share/clawtop`.

Key bindings: `tab`/`shift-tab` or `←` `→` to switch tabs, `1`–`4` to
jump, `r` to reload, `q` to quit.

## Status JSON (schema 2)

```json
{
  "schema": 2,
  "machine": "laptop",
  "ts": 1716688320,
  "session": { "pct": 42.1, "reset_at": 1716700000 },
  "week":    { "pct": 24.4, "reset_at": 1717100000 },
  "limit":   "allowed",
  "subscription": "max",
  "window":  "7d",
  "by_project": [
    { "name": "my-app", "path": "/home/you/code/my-app",
      "in": 255, "out": 172668, "cache_read": 18684, "cache_create": 8921 }
  ],
  "by_model": [
    { "model": "claude-opus-4-7",
      "in": 2751, "out": 632241, "cache_read": 53152478, "cache_create": 0 }
  ],
  "hourly_24h": [0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 107835, 7436, 0, 0, 0, 0, 0, 0, 178055]
}
```

Tiny payload (~1–5 KB per machine), self-describing, forward-compatible.

## Repo layout

```
clawtop/
├── cmd/
│   ├── clawtop/        # TUI binary
│   └── clawtopd/       # daemon binary + doctor subcommand
├── external/           # adapters to the outside world
│   ├── anthropic/      # Anthropic API client (creds + probe)
│   └── push/           # Pusher interface + local + ssh implementations
├── internal/
│   ├── domain/         # on-the-wire entities (Status, Window, Project, Model)
│   └── service/        # business logic
│       ├── collector/  # parse transcripts → aggregate
│       └── merger/     # merge N per-machine payloads
└── deploy/             # systemd units + INSTALL.md
```

## Cost

One Haiku call with `max_tokens: 1` per minute per daemon. On a Max plan
this is billed against the same subscription quota you're watching and
shows up in the very dashboard you're looking at. Three machines combined
add up to a single-digit number of cents per month.

## Contributing

PRs welcome. The whole thing is around 800 lines of Go.

## Credits

This project would not exist without
[Clawdmeter](https://github.com/HermannBjorgvin/Clawdmeter) by
[Hermann Bjorgvin](https://github.com/HermannBjorgvin), which is where I
learned that the Anthropic OAuth surface exposes the `unified` rate-limit
headers, and [ccusage](https://github.com/ryoppippi/ccusage) by
[ryoppippi](https://github.com/ryoppippi), which set the bar for what
transcript-derived analysis should look like.

## License

MIT — see [LICENSE](LICENSE).
