---
name: run-cows
description: Launch a local COWS testing instance (Go web server + rootless Podman) and smoke-test it. Use when asked to run, start, or verify the COWS app is working end-to-end, not just `go test`.
---

# Running a COWS testing instance

COWS is a single Go binary (`web` server + background reconcile/timeout
loops) backed by SQLite and a rootless Podman socket. Running the test
suite does not prove the app works — start the real binary and hit it with
curl (or a browser) to verify a change.

## Prerequisites

- Rootless Podman user socket must be active:
  `systemctl --user status podman.socket` (enable with
  `systemctl --user enable --now podman.socket` if not running).
- Build fresh before testing a change: `go build -o bin/cows ./cmd/cows`.

## Before starting: check for a process already listening

Port 8080 (COWS's default `COWS_LISTEN_ADDR`) is **not reliably free on this
machine** — an unrelated qBittorrent WebUI has been observed squatting on
it. Always check first, and never kill something you didn't start without
investigating what it is:

```sh
pgrep -af "bin/cows" | grep -v zsh   # is a COWS instance already running?
ss -ltn | grep :8080                 # is the default port taken by something else?
```

If 8080 is occupied by something that isn't your own COWS instance, pick a
different port with `COWS_LISTEN_ADDR` (8081 worked cleanly last time) rather
than touching the other process.

## Start it

Reuse the ignored `.cows-test/` directory for the database and mount roots —
this keeps runtime-owned files out of `go test ./...`'s package scan and
lets state persist across sessions:

```sh
COWS_LISTEN_ADDR=127.0.0.1:8081 \
COWS_DATABASE_PATH=./.cows-test/cows.db \
COWS_MOUNT_ROOT=./.cows-test/cows-mounts \
COWS_MOUNT_ARCHIVE_ROOT=./.cows-test/cows-mounts-archive \
COWS_BOOTSTRAP_ADMIN_USERNAME=admin \
COWS_BOOTSTRAP_ADMIN_PASSWORD='replace-this-with-a-long-password' \
nohup ./bin/cows > /tmp/cows-test.log 2>&1 &
disown
sleep 1
cat /tmp/cows-test.log   # confirm "COWS server started", no bind error
```

The default listener (`127.0.0.1`) is local-only. If the instance needs to be
reachable from another machine (e.g. the user testing from a browser on a
different host), bind all interfaces instead: `COWS_LISTEN_ADDR=0.0.0.0:8081`.
This is still plain HTTP with no TLS — fine for throwaway dev/test use, never
for real credentials or untrusted users (`docs/configuration.md` says the
same). Find LAN-reachable addresses with `ip -4 addr show | grep inet`.

**Bootstrap credentials only take effect on a database with zero users.**
`.cows-test/cows.db` from a prior session likely already has accounts (an
`admin`, and whatever else was created during testing) with unknown
passwords — the `COWS_BOOTSTRAP_ADMIN_*` vars above will silently do nothing
in that case. Check first:

```sh
sqlite3 .cows-test/cows.db "SELECT username, role, disabled FROM users;"
```

If you need guaranteed-known credentials and don't know the existing
password, delete `.cows-test/` first (it's local test data, not
production — but confirm with the user if unsure whose data it is) and start
against a fresh database.

## Verify it's actually working, not just up

```sh
curl -s http://127.0.0.1:8081/healthz   # {"status":"ok","database":"ok"}
curl -s http://127.0.0.1:8081/readyz    # {"status":"ok","database":"ok","runtime":"ok"}
```

`/readyz` (`runtime` field) only reports `"ok"` if it actually reached the
live Podman socket — a real end-to-end signal, not a mock. For anything
touching auth, log in through `/login` (GET for the CSRF token + form, POST
with `username`, `password`, `csrf_token`) rather than trusting a 200 on the
login page alone.

## Stop it

```sh
pgrep -af "bin/cows" | grep -v zsh
kill <pid>
```
