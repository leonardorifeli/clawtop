# clawtop

A multi-host TUI dashboard for your Anthropic Claude usage. Two small Go
binaries: a daemon (`clawtopd`) that lives on machines where you actually
run Claude, and a viewer (`clawtop`) that lives wherever you want to look at
the dashboard.

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

## What it is

`clawtop` answers four questions, in tabs:

1. **Limits** — how much of your 5-hour and 7-day rate-limit windows are
   you using, and when do they reset.
2. **Projects** — which working directories ate your token budget over the
   chosen window.
3. **Models** — Opus vs Sonnet vs Haiku split, with input, output, and
   cache token columns.
4. **Hourly** — a 24-hour sparkline of token consumption so you can spot
   the peak hours.

## What it isn't (and what to use instead)

clawtop is one of several open-source tools that read your local Claude
transcripts. Each has a different shape:

| Tool | Shape | Best when |
|------|-------|-----------|
| [`ccusage`](https://github.com/ryoppippi/ccusage) | On-demand CLI, prints tables | You want a fast snapshot from the shell |
| [`Claude-Code-Usage-Monitor`](https://github.com/Maciek-roboblog/Claude-Code-Usage-Monitor) | Always-on terminal monitor with burn-rate predictions | You want ML projections on a single host |
| [`ccflare`](https://claudefa.st/blog/tools/monitors/claude-code-usage-monitor) | Browser dashboard | You want graphs you can pan and zoom |
| [`Clawdmeter`](https://github.com/HermannBjorgvin/Clawdmeter) | ESP32 hardware display | You want a physical Clawd on your desk |
| **`clawtop`** | TUI in tmux, **multi-host aware**, creds-stay-local | You run Claude on more than one machine and want one place to look |

If you only run Claude on a single laptop and want a quick table, `ccusage`
is probably what you want. clawtop earns its keep when you have several
boxes (work laptop + personal desktop + dev VM) and want a unified view
without copying credentials around.

## Two differentiators, on purpose

**Multi-host aggregation.** Each machine runs a `clawtopd` that pushes a
small per-machine JSON to a central host. The viewer merges them: rate
limits are account-scoped so we keep the freshest, while token totals,
model splits, and hourly buckets are summed across hosts. Adding a fourth
machine is one binary install and one systemd enable.

**Credentials never travel.** The OAuth token in
`~/.claude/.credentials.json` is the keys to your account. `clawtopd` reads
it locally and talks to Anthropic locally. What ships over the wire is
already-derived percentages and token counts. The viewer host (your
homelab box, a Pi, whatever) sees only that derived JSON — never the token.

## Architecture

```
┌──────────────┐                          ┌──────────────────┐
│ laptop       │──clawtopd──┐             │ cypher           │
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
        │ HTTPS (1 Haiku token / minute)
        ▼
   api.anthropic.com
```

The daemon polls Anthropic every 60 s with a `max_tokens: 1` Haiku call —
fractions of a cent per day — and reads the
`anthropic-ratelimit-unified-*` response headers for the account-level
percentages. In the same pass it walks `~/.claude/projects/**/*.jsonl` to
aggregate per-project, per-model, and hourly token usage from your local
transcripts.

## Install

See [`deploy/INSTALL.md`](deploy/INSTALL.md) for the full walkthrough
(build, ssh setup, systemd units, lingering). The short version, once a
release exists:

```bash
# On every machine that runs Claude:
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh -s -- daemon --host=cypher

# On the machine where you want to view (can be the same):
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh -s -- viewer

# Then:
ssh cypher
tmux attach -t clawtop
```

## Configuration

`clawtopd`:

| flag | default | what |
|------|---------|------|
| `--creds` | `~/.claude/.credentials.json` | Claude OAuth credentials file |
| `--host` | `cypher` | ssh_config alias of the viewer host |
| `--remote-dir` | `/var/lib/clawtop` | remote directory for per-machine status JSON files |
| `--machine` | hostname | stable identifier; becomes `<machine>.json` on the viewer |
| `--projects` | `~/.claude/projects` | transcripts root (honors `$CLAUDE_CONFIG_DIR`) |
| `--window` | `168h` (7d) | aggregation lookback |
| `--interval` | `60s` | poll cadence |
| `--once` | `false` | probe once and exit (smoke test) |
| `--local-only` | `false` | print to stdout instead of pushing |
| `--skip-probe` | `false` | skip the Anthropic call, aggregate transcripts only |

`clawtop`:

| flag | default | what |
|------|---------|------|
| `--dir` | `/var/lib/clawtop` | directory of per-machine status JSON files |

Key bindings: `tab`/`shift-tab` or `←` `→` to switch tabs, `1`–`4` to jump,
`r` to reload, `q` to quit.

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
    { "name": "rifeli.dev", "path": "/home/rifeli/projects/personal/rifeli.dev",
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
│   └── clawtopd/       # daemon binary
├── external/           # adapters to the outside world
│   ├── anthropic/      # Anthropic API client (creds + probe)
│   └── push/           # SSH transport
├── internal/
│   ├── domain/         # on-the-wire entities (Status, Window, Project, Model)
│   └── service/        # business logic
│       ├── collector/  # parse transcripts → aggregate
│       └── merger/     # merge N per-machine payloads
└── deploy/             # systemd units + INSTALL.md
```

## Cost

One Haiku call with `max_tokens: 1` per minute per machine. On a Max plan
this is billed against the same subscription quota you're watching, and
shows up in the very dashboard you're looking at. Three machines together
add up to a single-digit number of cents per month.

## Contributing

PRs welcome. The whole thing is around 700 lines of Go.

## Credits

This project would not exist without
[Clawdmeter](https://github.com/HermannBjorgvin/Clawdmeter) by
[Hermann Bjorgvin](https://github.com/HermannBjorgvin), which is where I
learned that the Anthropic OAuth surface exposes a `unified` rate-limit
header. clawtop is what happens when you have no ESP32 and three boxes
that run Claude.

## License

MIT — see [LICENSE](LICENSE).
