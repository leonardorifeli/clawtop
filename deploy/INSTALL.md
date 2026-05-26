# Install

clawtop has two install shapes. Pick the one that matches your setup.

- **Single PC** — you run Claude and look at the dashboard on the same
  machine. No SSH, no remote host.
- **Multi PC** — you run Claude on N machines and want one dashboard
  that merges them. Adds a viewer host and SSH.

If you're not sure, start with **Single PC**. Upgrading to Multi PC
later is just installing the daemon on the other machines and pointing
them at a viewer host.

---

## Single PC

### 1. Install

```bash
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh
```

This drops `clawtopd` and `clawtop` into `~/.local/bin/`. Make sure that
directory is on your `$PATH`.

### 2. Verify

```bash
clawtopd doctor
```

Expected output:

```
OK    credentials   /home/you/.claude/.credentials.json   subscription=max
OK    anthropic     api.anthropic.com                     session=12.1% week=4.3% limit=allowed
OK    destination   local:/home/you/.local/share/clawtop  writable

doctor: all clear
```

If any line says `FAIL`, follow the `HINT` line that follows it.

### 3. Enable the daemon

```bash
mkdir -p ~/.config/systemd/user
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/deploy/clawtopd-local.service \
  -o ~/.config/systemd/user/clawtopd-local.service
systemctl --user daemon-reload
systemctl --user enable --now clawtopd-local
journalctl --user -u clawtopd-local -f
```

### 4. Open the dashboard

```bash
clawtop --dir=~/.local/share/clawtop
```

That's it. The daemon writes a fresh JSON every minute; the TUI redraws
every second.

---

## Multi PC

### Prerequisites

- Passwordless SSH from each Claude machine to the viewer host, using an
  alias defined in `~/.ssh/config`. Verify with `ssh <alias> true`.
- `tmux` on the viewer host.
- Linux/macOS only for now.

### 1. Install — on each Claude machine

```bash
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh -s -- daemon
clawtopd doctor --host=<viewer-alias>
```

Doctor should report `OK` for credentials, Anthropic, SSH, and the
remote destination. If SSH fails with `Permission denied (publickey)`,
run `ssh-copy-id <viewer-alias>` and try again.

### 2. Configure the daemon service

```bash
mkdir -p ~/.config/systemd/user
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/deploy/clawtopd-remote.service \
  -o ~/.config/systemd/user/clawtopd-remote.service
${EDITOR:-vi} ~/.config/systemd/user/clawtopd-remote.service
# Replace 'viewer-host' in ExecStart with your ssh_config alias.

systemctl --user daemon-reload
systemctl --user enable --now clawtopd-remote
journalctl --user -u clawtopd-remote -f
```

Repeat the install + service steps on every Claude machine. Each will
end up writing to `<viewer>:/var/lib/clawtop/<machine>.json`.

### 3. Install the viewer on the viewer host

```bash
# As root, once: create the directory the daemons push into.
sudo install -d -o $USER -g $USER /var/lib/clawtop

# As your user:
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh -s -- viewer

mkdir -p ~/.config/systemd/user
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/deploy/clawtop.service \
  -o ~/.config/systemd/user/clawtop.service
systemctl --user daemon-reload
systemctl --user enable --now clawtop

# Keep the user service alive without an interactive session:
sudo loginctl enable-linger $USER
```

### 4. Look at it

```bash
ssh <viewer-host>
tmux attach -t clawtop
# tab/← → to switch tabs · 1-4 to jump · r to reload · q to quit
# detach the tmux session with ctrl-b d
```

---

## Updating

```bash
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh
systemctl --user restart clawtopd-local   # or clawtopd-remote
# On the viewer host, also:
systemctl --user restart clawtop
```

---

## Troubleshooting

**`doctor: FAIL credentials  ... no such file`** — you haven't logged
into Claude on this machine yet. Run `claude` once to authenticate;
that writes `~/.claude/.credentials.json`. Re-run `clawtopd doctor`.

**`doctor: FAIL anthropic  401`** — your OAuth token expired and the
Claude CLI hasn't refreshed it yet. Run `claude` once to force a
refresh, then re-run doctor.

**`doctor: FAIL ssh  ... Permission denied (publickey)`** — your SSH
key is not authorized on the viewer host. Run
`ssh-copy-id <viewer-alias>` (you'll need a password the first time),
then re-run doctor.

**`doctor: FAIL ssh  ... could not resolve hostname`** — your
`~/.ssh/config` doesn't have a `Host <alias>` block with `HostName`
pointing to a reachable address. Add one:

```
Host viewer
    HostName 192.168.1.42
    User you
```

**`viewer says: no status files in /var/lib/clawtop`** — either the
daemon hasn't pushed yet (give it a minute), or the SSH push is
failing. Check `journalctl --user -u clawtopd-remote -f` on the daemon
machine. `clawtopd doctor --host=<viewer-alias>` will diagnose it.

**Rate-limit percentages disagree between machines** — the rate-limit
is account-scoped on Anthropic's side, so they should be nearly
identical. A persistent gap usually means one machine's clock is wrong.
The viewer shows the freshest report.
