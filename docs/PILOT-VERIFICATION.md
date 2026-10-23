# Pilot handoff — October 23, 2026

CompanyCam remains the authoritative record. No CompanyCam settings or customer data were changed, and no customer photos were imported.

## Deployed pilot

- Fork: https://github.com/DanTerepka/dukecam
- Review: https://github.com/DanTerepka/dukecam/pull/1 (draft)
- Branch: `fix/radpad-pilot`; reviewed core code revision `0fa3706`.
- URL: https://ubuntu5.tailfe58d2.ts.net:4011/ (tailnet only).
- Access: only `dan@ir-ga.com`, verified by Tailscale Serve. The app accepts identity headers only on its private Unix listener. Neither app nor database has a published TCP port.
- Checkout: `/home/ubuntu5/radpad-dukecam-pilot/repo`; separate data, socket, secrets, database, and backup paths under `/home/ubuntu5/radpad-dukecam-pilot`.
- The nonsecret `runtime.env` pins the deployed image revision. The app runs as UID/GID 10001 with a read-only root filesystem, internal-only database network, and 2 GiB memory cap.

## Checks executed

- Go test suite and race detector passed locally and against disposable PostgreSQL 16, including sixteen simultaneous requests for the same upload.
- Thirteen JavaScript regression tests passed. Code, JavaScript, and security reviews cleared their identified deployment blockers after fixes.
- Build/static checks passed. Go was updated to 1.27.3 and dependencies patched. govulncheck reported zero reachable vulnerabilities; one advisory exists in a required module whose vulnerable code is not called.
- GitHub Actions passed the Go/PostgreSQL/JavaScript/build/static/vulnerability checks for the core change.
- Chrome displayed the styled admin and project pages; project creation through the browser succeeded. Browser-extension file upload was blocked by its file-access setting, which was not broadened. Browser/real-phone file selection and end-to-end offline capture are not claimed as tested.
- Live HTTPS API tests: 100 small synthetic PNGs, four 24.5 MP synthetic JPEGs, one initial large JPEG, and eight concurrent requests for one additional photo produced 106 photos and exactly 106 durable receipts.
- Reusing an upload ID with different bytes returned 409; cross-origin upload returned 403. An upload replay after app stop/start returned its original ID and filename.
- Anonymous Unix requests and unapproved identities returned 403. Client-supplied identity headers through Serve were overwritten with the verified identity. A separate real unauthorized tailnet account was not available for testing; its denial is covered by middleware tests and direct Unix checks.
- Disabled sharing returned 404. Health returned 204 after restart. Container had no OOM and no automatic restarts; peak recorded memory during the synthetic burst was about 137 MiB, below its 2 GiB limit. Later restart reset the cgroup peak.
- Backup `20261023T111536Z.tKHMWe` was restored into a disposable database/media directory: 106 original files matched 106 receipt checksums.
- An encrypted copy was saved off-server on the Mac at `/Users/danterepka/Documents/Fb Ads/RadPad-Pilot-Backups/2026-10-23-pilot-backup.tar.gz.age`, encrypted to the existing Mac SSH public key. Local decryption verified both archive checksums; a copy round trip to ubuntu5 was independently restored and all 106 originals verified again. Keep the existing SSH private key recoverable; no new credential was created or copied into the repository.

## Pilot limits and next acceptance checks

JPEG/PNG only; 25 MB and 25 megapixels maximum per photo. HEIC, video, custom burst capture, inspections/integration writes, public sharing, rotation and deletion are disabled. Reopening a project while completely offline is unsupported; already queued IndexedDB bytes persist, and uploads resume when the app is reopened online. Keep originals and CompanyCam copies: browser storage is not a backup.

Use `radpad-first-field-test` for the first real-phone trial. Test camera/roll selection, airplane mode with the page open, reconnection, close/reopen online, interrupted reception and low storage. Do not add crew, sensitive customer evidence, or retire CompanyCam until those checks pass and access/job permissions are reviewed.

Backups are manual for this pilot; the verified Mac copy is a point-in-time snapshot, not continuous protection. Follow `docs/PILOT.md` for backup and restore checks after pilot sessions. Crew access, automatic off-server backups, revocable customer links, annotations, video and migration remain separate promotion work.
