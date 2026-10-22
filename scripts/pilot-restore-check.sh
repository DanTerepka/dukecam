#!/usr/bin/env bash
# Restore only to disposable scratch; never restore over the pilot.
set -Eeuo pipefail
umask 077
[[ $# == 1 ]] || { echo 'Usage: pilot-restore-check.sh BACKUP_DIRECTORY' >&2; exit 2; }
backup=$(cd -- "$1" && pwd)
repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo"
compose=(docker compose)
# Nonsecret image revision only; Compose otherwise reads its normal .env.
if [[ -f ../runtime.env ]]; then compose+=(--env-file ../runtime.env); fi
compose+=(-p radpad-dukecam-pilot -f compose.pilot.yml)
scratch=$(mktemp -d "${TMPDIR:-/tmp}/radpad-restore.XXXXXXXX")
database="radpad_restore_$$_${RANDOM}"
created=0
cleanup() {
  status=$?
  if (( created )); then
    if ! "${compose[@]}" exec -T db dropdb -U radpad "$database"; then
      echo "Could not remove scratch database $database" >&2; status=1
    fi
  fi
  rm -rf -- "$scratch"
  exit "$status"
}
trap cleanup EXIT
# Verify both archive checksums before PostgreSQL restore or extraction.
python3 - "$backup" "$scratch" <<'PY'
import hashlib, pathlib, sys, tarfile
root, scratch = map(pathlib.Path, sys.argv[1:])
expected = {}
for line in (root / 'SHA256SUMS').read_text().splitlines():
    digest, name = line.split('  ', 1)
    if name not in ('database.dump', 'media.tar.gz') or name in expected:
        raise SystemExit('Unexpected manifest entry')
    expected[name] = digest
if set(expected) != {'database.dump', 'media.tar.gz'}: raise SystemExit('Incomplete manifest')
for name, digest in expected.items():
    h = hashlib.sha256()
    with (root / name).open('rb') as source:
        for chunk in iter(lambda: source.read(1024 * 1024), b''): h.update(chunk)
    if h.hexdigest() != digest: raise SystemExit(f'Checksum mismatch: {name}')
media = scratch / 'media'
media.mkdir()
with tarfile.open(root / 'media.tar.gz', 'r:gz') as archive:
    for member in archive:
        target = media / member.name
        if not target.resolve().is_relative_to(media.resolve()) or not (member.isfile() or member.isdir()):
            raise SystemExit('Unsafe archive member')
        if member.isdir(): target.mkdir(parents=True, exist_ok=True)
        else:
            target.parent.mkdir(parents=True, exist_ok=True)
            with archive.extractfile(member) as src, target.open('wb') as dst:
                while chunk := src.read(1024 * 1024): dst.write(chunk)
PY
"${compose[@]}" exec -T db createdb -U radpad "$database"
created=1
"${compose[@]}" exec -T db pg_restore -U radpad -d "$database" --no-owner --exit-on-error < "$backup/database.dump"
"${compose[@]}" exec -T db psql -X -U radpad -d "$database" -At -v ON_ERROR_STOP=1 <<'SQL' > "$scratch/evidence.json"
SELECT json_build_object(
 'projects', (SELECT count(*) FROM projects),
 'photos', (SELECT count(*) FROM photos),
 'receipts', (SELECT count(*) FROM upload_receipts),
 'files', (SELECT coalesce(json_agg(row_to_json(x)), '[]'::json) FROM
   (SELECT p.id, p.storage_path, r.checksum FROM photos p LEFT JOIN upload_receipts r ON r.photo_id=p.id ORDER BY p.id) x));
SQL
python3 - "$scratch" <<'PY'
import hashlib, json, pathlib, sys
scratch = pathlib.Path(sys.argv[1])
evidence = json.loads((scratch / 'evidence.json').read_text())
checked = 0
for photo in evidence['files']:
    path = pathlib.PurePosixPath(photo['storage_path'])
    if not path.is_relative_to('/data') or '..' in path.parts:
        raise SystemExit(f'Unexpected storage path for photo {photo["id"]}')
    restored = scratch / 'media' / path.relative_to('/data')
    if not restored.is_file(): raise SystemExit(f'Missing original for photo {photo["id"]}')
    if photo['checksum']:
        h = hashlib.sha256()
        with restored.open('rb') as source:
            for chunk in iter(lambda: source.read(1024 * 1024), b''): h.update(chunk)
        if h.hexdigest() != photo['checksum'].strip():
            raise SystemExit(f'Original checksum mismatch for photo {photo["id"]}')
        checked += 1
if evidence['photos'] != evidence['receipts'] or checked != evidence['photos']:
    raise SystemExit('Every pilot photo must have exactly one verified receipt')
print(f'Restored projects={evidence["projects"]}, photos={evidence["photos"]}, receipts={evidence["receipts"]}; originals verified={checked}')
if not checked: print('Empty backup: restore succeeded but no photo evidence was exercised.')
PY
