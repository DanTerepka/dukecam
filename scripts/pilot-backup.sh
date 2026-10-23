#!/usr/bin/env bash
# Run on ubuntu5 from any directory. Credentials stay in Compose secrets.
set -Eeuo pipefail
umask 077
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo"
compose=(docker compose)
# Nonsecret image revision only; Compose otherwise reads its normal .env.
if [[ -f ../runtime.env ]]; then compose+=(--env-file ../runtime.env); fi
compose+=(-p radpad-dukecam-pilot -f compose.pilot.yml)
[[ -f compose.pilot.yml ]] || { echo 'Missing compose.pilot.yml' >&2; exit 1; }
mkdir -p ../backups
backup=$(mktemp -d "../backups/$(date -u +%Y%m%dT%H%M%SZ).XXXXXX")
restart=0
finish() {
  status=$?
  if (( restart )); then
    if ! "${compose[@]}" start app; then
      echo 'ERROR: restart app manually before leaving.' >&2
      status=1
    fi
  fi
  if (( status != 0 )); then echo "INCOMPLETE backup retained: $backup" >&2; fi
  exit "$status"
}
trap finish EXIT
# Refuse to change an already-stopped app's state.
[[ -n $("${compose[@]}" ps --status running -q app) ]] || { echo 'App must be running before backup.' >&2; exit 1; }
restart=1
"${compose[@]}" stop app
"${compose[@]}" exec -T db pg_dump -U radpad -d radpad_pilot -Fc > "$backup/database.dump"
"${compose[@]}" run --rm --no-deps -T --entrypoint tar app -C /data -czf - . > "$backup/media.tar.gz"
python3 - "$backup" <<'PY'
import hashlib, pathlib, sys
root = pathlib.Path(sys.argv[1])
with (root / 'SHA256SUMS').open('w') as out:
    for name in ('database.dump', 'media.tar.gz'):
        h = hashlib.sha256()
        with (root / name).open('rb') as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b''): h.update(chunk)
        out.write(f'{h.hexdigest()}  {name}\n')
PY
printf 'Complete backup: %s\nRun scripts/pilot-restore-check.sh %q before relying on it.\n' "$backup" "$backup"
