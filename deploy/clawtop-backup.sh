#!/usr/bin/env sh
# Daily snapshot of /var/lib/clawtop/*.json into
# /var/lib/clawtop/archive/YYYY-MM-DD/, so trend analysis can reach beyond
# the 7d window the daemon keeps in memory. Idempotent.
#
# Runs on the viewer host. Enable via clawtop-backup.timer (systemd) or a
# plain crontab entry: `0 0 * * * /usr/local/bin/clawtop-backup.sh`.

set -eu

SRC="${CLAWTOP_DIR:-/var/lib/clawtop}"
DEST="$SRC/archive/$(date +%Y-%m-%d)"

if [ ! -d "$SRC" ]; then
  echo "clawtop-backup: source $SRC missing" >&2
  exit 1
fi

mkdir -p "$DEST"
count=0
for f in "$SRC"/*.json; do
  [ -e "$f" ] || continue
  cp -p "$f" "$DEST/"
  count=$((count + 1))
done

echo "clawtop-backup: archived $count machine snapshots to $DEST"
