# Rad Pad DukeCam pilot

CompanyCam remains the authoritative record. Keep every job photo in CompanyCam until the phone, access, recovery, and restore acceptance checks below pass and Dan approves replacing it. This deployment is a pilot, not a production replacement.

## Isolation and access

The Ubuntu5 checkout is `/home/ubuntu5/radpad-dukecam-pilot/repo`. Use `docker compose -p radpad-dukecam-pilot -f compose.pilot.yml` for all operations from this directory. If `../runtime.env` exists, include `--env-file ../runtime.env` immediately after `docker compose`; the backup/check scripts do so automatically. Otherwise Compose reads its normal `.env`. Keep only the nonsecret `PILOT_REVISION` image tag in those files. Set that tag to the reviewed deployed revision before operating. The pilot has its own PostgreSQL database `radpad_pilot`, PostgreSQL user `radpad`, and storage. No app or database TCP port is published.

Runtime media is in `../data`, owned by UID 10001. The app communicates through `../run/dukecam.sock`, owned by UID 10001 and mode 0600. The host run directory is mode 0700. Tailscale Serve proxies that socket at `https://ubuntu5.tailfe58d2.ts.net:4011`. Access must be restricted to Dan through the applicable tailnet policy and app authorization. Do not enable Funnel or broaden permissions to make access work.

Keep credentials in `../secrets` outside Git. Do not print container environments or copy credentials into this runbook. The app and database must use their established secret-file configuration. Never expose the socket with a TCP relay.

## Supported pilot workflow and limits

- JPEG and PNG only, maximum 25 MB and 25 megapixels per photo.
- Native phone camera/file picker; burst capture and video are unsupported.
- Inspections, public sharing, photo rotation, and deletion are disabled.
- Pending uploads persist in IndexedDB. The open page can queue while offline and automatically resume after connectivity returns. Reopening the app while offline is unsupported.
- Browser storage can be cleared or evicted. A queued photo is not a server backup; retain the original and its CompanyCam copy.

## Routine operation

Run commands from the checkout. Check `docker compose -p radpad-dukecam-pilot -f compose.pilot.yml ps` and bounded logs with `docker compose -p radpad-dukecam-pilot -f compose.pilot.yml logs --tail=50 app db`. Check available disk space on both runtime storage and the backup destination. Investigate upload errors before collecting more pilot evidence.

For restart, use `docker compose -p radpad-dukecam-pilot -f compose.pilot.yml restart app`. For redeployment, first create and verify a backup, record the current Git commit and image identifier, review the intended commit and configuration, then use `docker compose -p radpad-dukecam-pilot -f compose.pilot.yml up -d --build`. Recheck authorized access, denied access, photo upload, and receipt integrity. Never use prune or `down -v`.

## Backup and independent restore verification

Run `scripts/pilot-backup.sh` on Ubuntu5. It stops only the app, captures a custom-format PostgreSQL dump and the complete media directory, writes SHA256SUMS, and restarts the app using an exit trap. The database stays running. The app must initially be running; the script refuses to silently change an intentionally stopped deployment. Backup output goes to a new timestamped directory under `../backups`. A failed backup stays available for investigation and is explicitly reported incomplete. Check the app restarted successfully.

Run `scripts/pilot-restore-check.sh BACKUP_DIRECTORY` with the backup directory printed by the backup script. It verifies archive hashes, restores into a newly generated disposable database, safely extracts media into a disposable directory, counts projects/photos/receipts, and verifies every restored original against its upload receipt checksum. Missing files, missing receipts, or checksum mismatches fail the check. An empty backup is reported as empty and is not evidence that photo recovery works. The script drops only its own scratch database and removes only its own generated scratch directory; it never restores over the live pilot.

The scripts require Bash, Docker Compose, Python 3.9+, and the supplied container tools. Run them as the host account authorized for Docker. Secrets and configuration are deliberately excluded: separately secure the secret files and record the reviewed Compose version, Git commit, and image identifier needed to rebuild. A backup on the same host does not protect against host loss. Arrange an encrypted off-server copy and verify a restore from that copy before treating the pilot as dependable.

For an actual disaster restore, stop the pilot app and preserve the damaged database/media as evidence. Use the exact reviewed application version. Restore the verified dump into a new database and media into a new UID-10001 directory; verify checksums there before switching the configuration to those recovered resources. This is a separate, explicitly approved recovery operation. Do not use the check script as a live restore tool.

## Rollback

Keep the prior application image/commit and a verified matching database/media backup. Stop the app, select that prior version, and restart using the same isolated Compose project. If a schema migration prevents compatibility, restore into separate replacement database/media resources as described above and switch only after verification. Preserve failed runtime data for diagnosis. Do not overwrite or delete the only recoverable copy, and do not change CompanyCam.

## Acceptance gates

Use test jobs without sensitive customer information first. Record observed results rather than assuming a passed unit test establishes phone reliability.

1. Authorized Dan access succeeds. An unauthorized tailnet identity and an unauthenticated direct request fail, including media URLs and mutations. No public access or host TCP ports exist.
2. Upload at least 100 supported photos on a real iPhone. Confirm originals, timestamps, job assignment, and server receipt checksums. Confirm unsupported formats and oversized images fail clearly.
3. Queue photos in airplane mode with the page open; reconnect and verify automatic completion. Interrupt an upload response, retry, and verify one original/record per client upload ID. Reuse an ID with different content and verify rejection.
4. Close and reopen online with pending queued photos; restart the app server; verify recovery. Exercise phone storage pressure and interrupted reception while retaining CompanyCam originals.
5. Back up a populated job and run the independent restore check. Repeat using the encrypted off-server copy. Record counts and integrity outcomes.
6. Pilot with Dan before adding crew. Review individual accounts/job permissions, revocable customer sharing, annotations, video, exports, and support ownership as separate work before CompanyCam retirement.

## Development checks

Run the repository Go tests and Node upload-queue tests. Database integration tests require a disposable real PostgreSQL database through `TEST_DATABASE_URL`; keep credentials out of logs and never target the pilot database. CI should execute these tests along with its build/static checks. Current deployment/test evidence belongs in the handoff record; this runbook itself claims no executed phone or disaster-recovery results.
