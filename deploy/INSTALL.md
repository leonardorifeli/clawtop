# Install

clawtop has two install shapes:

- **Single PC** — you run Claude and look at the dashboard on the same
  machine. No SSH, no remote host.
- **Multi PC** — you run Claude on N machines and want one dashboard
  that merges them on a separate viewer host. Daemons push JSON via
  SSH; viewer renders.

You can also **mix both**: a machine can run a local daemon (for a
local-only view) and a remote daemon (to feed the multi-host viewer)
at the same time. See [Running local and remote side by side](#running-local-and-remote-side-by-side).

---

## Single PC

### 1. Install

```bash
curl -fsSL https://raw.githubusercontent.com/leonardorifeli/clawtop/main/install.sh | sh
```

Drops `clawtopd` and `clawtop` into `~/.local/bin/`, and the systemd
user units into `~/.config/systemd/user/`. Make sure `~/.local/bin` is
on your `$PATH`.

If your terminal hard-wraps long URLs on paste (WaveTerm, some xterms),
download first then run:

```bash
git clone https://github.com/leonardorifeli/clawtop /tmp/c
sh /tmp/c/install.sh
```

### 2. Verify

```bash
clawtopd doctor
```

Expected:

```
OK    credentials   /home/you/.claude/.credentials.json   subscription=max
OK    anthropic     api.anthropic.com                     session=12.1% week=4.3%
OK    destination   local:/home/you/.local/share/clawtop  writable

doctor: all clear
```

Any `FAIL` line is followed by a `HINT` line with the fix.

### 3. Enable the daemon

```bash
systemctl --user daemon-reload
systemctl --user enable --now clawtopd-local
journalctl --user -u clawtopd-local -f
```

### 4. Open the dashboard

```bash
clawtop --dir=~/.local/share/clawtop
```

---

## Multi PC

### Prerequisites

- An always-on machine to act as the viewer host (homelab box, NAS, Pi,
  old NUC, anything). It does not need Claude installed.
- Passwordless SSH from each Claude machine to the viewer host, using
  an alias in `~/.ssh/config`. The alias must connect as a user who
  owns `/var/lib/clawtop` on the viewer.
- `tmux` on the viewer host.
- Linux/macOS only for now.

### 1. Viewer host (once)

```bash
ssh viewer-host

# Create the directory the daemons will push into.
sudo install -d -o $USER -g $USER /var/lib/clawtop

# Install viewer binary + systemd unit.
git clone https://github.com/leonardorifeli/clawtop /tmp/c
sh /tmp/c/install.sh viewer

systemctl --user daemon-reload
systemctl --user enable --now clawtop
sudo loginctl enable-linger $USER

exit
```

### 2. Each Claude machine

```bash
# Verify ~/.ssh/config has a Host block for the viewer:
#   Host viewer
#       HostName 192.168.x.x
#       User <the-user-on-viewer-host>
ssh viewer true && echo "ssh ok"
# If "Permission denied (publickey)": ssh-copy-id viewer

# Install daemon + units.
git clone https://github.com/leonardorifeli/clawtop /tmp/c
sh /tmp/c/install.sh daemon

# Point the remote unit at your viewer alias.
${EDITOR:-nano} ~/.config/systemd/user/clawtopd-remote.service
# Change `--host=viewer-host` in ExecStart to your actual alias.

clawtopd doctor --host=<your-alias>           # 4 OKs expected
systemctl --user daemon-reload
systemctl --user enable --now clawtopd-remote
journalctl --user -u clawtopd-remote -n 3 --no-pager
```

Repeat for every Claude machine. Each ends up writing
`<viewer>:/var/lib/clawtop/<machine>.json`.

### 3. Look at it

```bash
ssh viewer-host
tmux attach -t clawtop
# tab/← →: switch tabs (Limits/Projects/Models/Hosts/Sessions/Hourly)
# 1-6:     jump to tab directly
# t:       toggle dense dashboard / tabbed
# f:       cycle host filter (all → first → second → ... → all)
# j/k g G PgUp/PgDn: scroll project list
# r:       force reload
# q:       quit
# detach the tmux session with ctrl-b d (TUI keeps running)
```

---

## Running local and remote side by side

A daemon machine can run both `clawtopd-local` (writes to
`~/.local/share/clawtop/`) and `clawtopd-remote` (pushes to the viewer
host). They write to different directories and don't conflict. The
machine appears on the central viewer's HOSTS panel **and** has its own
local dashboard.

```bash
# On the Claude machine, after the remote daemon is already enabled:
systemctl --user enable --now clawtopd-local

# Open a local-only window in any terminal:
clawtop --dir=~/.local/share/clawtop
```

Cost is 2 probes/minute instead of 1 (fractions of a cent per day).

If you only want to look at one machine's data from inside the merged
viewer instead, use the filter — start the viewer with
`clawtop --machine=omen`, or press `f` while it's running to cycle
through hosts.

---

## Updating

Re-run the same `install.sh`. It overwrites binaries with the latest
release and **automatically restarts any enabled clawtop service**:

```bash
sh /tmp/c/install.sh                # or: install.sh daemon | viewer
```

The script is idempotent. Service unit files are not overwritten when
they differ from the shipped versions (they land as `*.service.new` so
your edits survive); review and replace manually if you want the new
default.

---

## Troubleshooting

**`doctor: FAIL credentials  ... no such file`** — you haven't logged
into Claude on this machine yet. Run `claude` once; that writes
`~/.claude/.credentials.json`. Re-run doctor.

**`doctor: FAIL anthropic  401`** — your OAuth token expired and the
Claude CLI hasn't refreshed it yet. Run `claude` once to force a
refresh, then re-run doctor.

**`doctor: FAIL ssh  ... Permission denied (publickey)`** — your SSH
key is not authorized on the viewer host for the user the alias
connects as. Run `ssh-copy-id <viewer-alias>`. If the alias was
connecting as the wrong user (e.g. `User rifeli` when the viewer's
account is `cypher`), edit `~/.ssh/config` to set `User <correct>`
first.

**`doctor: FAIL ssh  ... could not resolve hostname`** — your
`~/.ssh/config` doesn't have a `Host <alias>` block, or DNS for the
literal hostname is failing. Add a block with `HostName <ip>`:

```
Host viewer
    HostName 192.168.1.42
    User you
```

**Viewer shows `no status files in /var/lib/clawtop`** — either the
daemon hasn't pushed yet (give it a minute), or the SSH push is failing
silently. Check `journalctl --user -u clawtopd-remote -f` on the
daemon machine and run `clawtopd doctor --host=<viewer-alias>`.

**Rate-limit shows 0% / `plan: ?` after running fine for a while** —
the Anthropic probe failed and v0.3+ daemons keep the last-known-good
values on the next push, so this shouldn't happen on v0.3.1+. If it
does, `journalctl --user -u clawtopd-remote -n 30` will show the
upstream error.

**Long `curl ... | sh` commands break on paste** — your terminal is
hard-wrapping the URL. Use the `git clone /tmp/c` pattern above
instead.
