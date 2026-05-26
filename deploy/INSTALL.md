# Install

clawtop has two pieces. The **daemon** (`clawtopd`) runs on every machine
where you actually use Claude — it reads the local OAuth credentials and
the local transcripts, and pushes a small JSON to a chosen viewer host.
The **viewer** (`clawtop`) runs on that viewer host inside a tmux session.

You can install both on the same machine if you want (set `--host` to
`localhost` and skip the SSH setup), but the typical layout is N
daemon-hosts → 1 viewer-host.

## Prerequisites

- Go ≥ 1.22 to build from source (only needed until a release is published).
- Passwordless SSH from each daemon-host to the viewer-host, using an
  alias defined in `~/.ssh/config`.
- `tmux` on the viewer-host.
- A user systemd that can keep services alive across logouts. On Linux,
  `sudo loginctl enable-linger $USER` on the viewer-host does this.

## 1. Build (during development; release binaries will land later)

```bash
git clone https://github.com/leonardorifeli/clawtop
cd clawtop
go build -o bin/clawtopd ./cmd/clawtopd
go build -o bin/clawtop  ./cmd/clawtop

# Cross-compile the viewer for the viewer-host's arch if it differs:
GOOS=linux GOARCH=arm64 go build -o bin/clawtop-arm64 ./cmd/clawtop
```

## 2. Daemon — on each machine that runs Claude

```bash
install -Dm755 bin/clawtopd ~/.local/bin/clawtopd
install -Dm644 deploy/clawtopd.service ~/.config/systemd/user/clawtopd.service

# The unit defaults to --host=cypher. Edit it if your viewer-host alias differs:
${EDITOR:-vi} ~/.config/systemd/user/clawtopd.service

systemctl --user daemon-reload
systemctl --user enable --now clawtopd.service
journalctl --user -u clawtopd -f
```

Smoke-test before enabling the service:

```bash
clawtopd --once --local-only          # prints one status JSON to stdout
clawtopd --once                       # actually pushes to the viewer host
```

The daemon uses your ssh-agent — verify `ssh cypher true` works first.

## 3. Viewer — on the host where you want the dashboard

```bash
# As root, once: create the directory the daemon will push into.
sudo install -d -o $USER -g $USER /var/lib/clawtop

# Copy the viewer binary in:
scp bin/clawtop cypher:~/.local/bin/clawtop
ssh cypher 'chmod +x ~/.local/bin/clawtop'

# Install the systemd unit that runs the viewer in a persistent tmux session:
scp deploy/clawtop.service cypher:~/.config/systemd/user/clawtop.service
ssh cypher 'systemctl --user daemon-reload && systemctl --user enable --now clawtop.service'

# Keep the user-service alive without an interactive session:
ssh cypher 'sudo loginctl enable-linger $USER'
```

## 4. Look at it

```bash
ssh cypher
tmux attach -t clawtop
# tab/← → to switch tabs · 1-4 to jump · r to reload · q to quit
# detach the tmux session with ctrl-b d
```

## Multi-host setup (the point of clawtop)

For each additional Claude-using machine, repeat **section 2**. Each
daemon writes to `/var/lib/clawtop/<machine>.json` on the viewer-host;
the viewer merges them automatically.

Use `--machine=<id>` if your hostnames are not human-friendly:

```bash
clawtopd --machine=work-laptop --host=cypher
```

## Updating

```bash
# Daemon hosts:
go build -o bin/clawtopd ./cmd/clawtopd
install -m755 bin/clawtopd ~/.local/bin/clawtopd
systemctl --user restart clawtopd

# Viewer host:
go build -o bin/clawtop ./cmd/clawtop
scp bin/clawtop cypher:~/.local/bin/clawtop
ssh cypher 'systemctl --user restart clawtop'
```

## Troubleshooting

**`could not open a new TTY` when running `clawtop`** — the viewer is a
TUI; it needs a real terminal. The systemd unit launches it inside tmux
for exactly this reason. Don't run it as `ExecStart=clawtop` directly.

**Daemon logs say `probe: 401`** — your OAuth token expired. Run
`claude` locally to refresh the credentials file; the daemon will pick up
the new token on its next poll.

**Viewer says `no status files in /var/lib/clawtop`** — either the daemon
hasn't pushed yet (give it a minute), or the SSH push is failing. Check
`journalctl --user -u clawtopd -f` on the daemon-host.

**Rate-limit percentages disagree between machines** — the rate-limit is
account-scoped on Anthropic's side, so they should be nearly identical.
A persistent gap means one daemon's clock is wrong, or one host is being
rate-limited differently (rare but possible during regional incidents).
The viewer uses the freshest report.
