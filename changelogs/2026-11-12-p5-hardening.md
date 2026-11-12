# 2026-11-12 — P5: hardening (one daemon, units, backup, search, export)

P5 turns the hand-started stack into something that survives a reboot: `irisd` serves the built SPA itself,
two systemd **user** units own the processes, Postgres full-text answers search, and every room can be
exported as markdown. Backup and restore are one command each and were restored for real.

The live units were **installed but deliberately not enabled** in this session (see “What still needs a
human”). Search was proven on throwaway databases; the live database needs the new migration.

## A. One process serves the UI

`irisd` now mounts the built SPA from `IRIS_WEB_DIR` (default `web/dist`, relative to the unit's
`WorkingDirectory`).

| Request | Answer |
|---|---|
| `/assets/<hash>.js`, `/assets/<hash>.css` | the file, `Cache-Control: public, max-age=31536000, immutable` |
| `/`, `/rooms`, `/r/<id>` (reload included) | `index.html`, `Cache-Control: no-cache, must-revalidate` |
| `/assets/missing.js` | plain 404, never the shell |
| `/api/nope` without a cookie | `401` JSON |
| `/api/nope` with a cookie | `404` JSON (`{"error":"That API path is not here."}`) |
| any non-API path when `web/dist` is absent | plain 404, API keeps answering; boot log warns |

The auth middleware now protects only `/api/*`. Static files must be public or the login screen could never
load its own JavaScript. Design detail: a missing hashed asset is a 404, not a fallback to HTML, so a broken
build cannot look like a working app with a blank screen.

Measured (`scripts/p5-acceptance.sh`, real build served by `bin/irisd`):

```
SPA: / 1001 bytes; /assets/index-BbvVBmA2.js immutable; /api/nope 401 then 404 JSON
```

Production is now two processes: `irisd` + the voice sidecar. No Vite process on 4175.

## B. systemd user units (`deploy/`)

| File | What it is |
|---|---|
| `deploy/iris.service` | `bin/irisd`, `WorkingDirectory=…/iris`, `EnvironmentFile=%h/.config/iris/env`, `Restart=always`, `RestartSec=2`, `After=network-online.target` |
| `deploy/iris-voice.service` | `voice/server.py` with `~/Documents/projects/voice-hud/bridge/.venv/bin/python`, same restart policy, `After=iris.service` |
| `deploy/iris.env.example` | the only place secrets live: `IRIS_DATABASE_URL`, `IRIS_PASSPHRASE`, `IRIS_VOICE_URL` (plus a commented `IRIS_COOKIE_SECURE` for HTTPS) |
| `deploy/install.sh` | copies units, creates the env file once (0600), `daemon-reload`, `enable --now` when the env is real |

`chmod 600 ~/.config/iris/env` is both documented and enforced by the script. The units themselves contain no
secret. `install.sh` is idempotent: a second run keeps the existing env file and re-copies/enables the same
units. It refuses to start against the template placeholder instead of crash-looping.

Measured:

```
$ ./deploy/install.sh --no-start     # first and second run, same result
units installed in /home/aryadhira/.config/systemd/user
env file kept as is: /home/aryadhira/.config/iris/env (never overwritten)
systemd --user reloaded
$ systemd-analyze --user verify ~/.config/systemd/user/iris.service ~/.config/systemd/user/iris-voice.service
(no output, exit 0)
$ stat -c '%a %n' ~/.config/iris/env
600 /home/aryadhira/.config/iris/env
```

A fresh `$HOME` run created `600` env + units and a second run was a no-op on the env file.

### Install (for a human)

```bash
cd ~/Documents/projects/iris
(cd web && npm run build)                         # produces web/dist
go build -o bin/irisd ./cmd/irisd                 # produces bin/irisd
~/go/bin/migrate -path db/migrations \
  -database "$(awk -F= '$1=="IRIS_DATABASE_URL"{print $2}' ~/.config/iris/env)" up
$EDITOR ~/.config/iris/env                        # passphrase + real DB URL, chmod is already 600
./deploy/install.sh                               # enable --now
systemctl --user status iris.service iris-voice.service
curl -fsS http://127.0.0.1:3300/api/health
```

## C. Backup and restore

`scripts/backup.sh` makes `backups/iris-YYYYmmdd-HHMM.tar.gz` containing `db.dump` (`pg_dump -Fc`),
`rooms/` (the whole room store), and `manifest.txt` with the source row counts. It verifies with `gzip -t`
and `tar -tzf` before reporting success, then keeps the newest 7 archives. Postgres runs in Docker here, so
the script uses `docker exec ailab-postgres pg_dump` when no host `pg_dump` exists; `IRIS_PG_CONTAINER`
overrides the container.

`scripts/restore.sh <archive> <database> [--force] [--data-dir DIR]`:

- refuses `postgres`/`template*`;
- refuses the live database named in `IRIS_DATABASE_URL` without `--force`;
- refuses any existing target without `--force`; with `--force` it drops and recreates it;
- restores via `pg_restore --no-owner --no-acl --exit-on-error`, merges `rooms/` into the data dir
  (printed before writing), then prints the restored row counts.

Measured on the real acceptance database (real room, attachment and Hermes reply):

```
backup: iris-20261112-1041.tar.gz, 4404 bytes, source rows 1|2|1|0
restore: iris_restore_check rows 1|2|1|0 — identical to source
dropped iris_restore_check
```

The Go parity test (`internal/backup/parity_test.go`, opt-in via `IRIS_BACKUP_TEST_DSN`) does the same round
trip itself, checks the room file byte-for-byte, and drops the target:

```
row-count parity: rooms=1 messages=2 files=1 continuations=0
dropped iris_restore_check
```

The test is skipped in a plain `go test ./...` so it can never touch a database by accident. The acceptance
script sets the variable and runs it every time.

## D. Search

Migration `000007_search` adds:

```sql
ALTER TABLE messages ADD COLUMN search_vector tsvector
  GENERATED ALWAYS AS (to_tsvector('indonesian'::regconfig, body)) STORED;
CREATE INDEX messages_search_idx ON messages USING GIN (search_vector);
```

No new dependency, no application-side reindex, and the vector can never drift from `body`.

`GET /api/search?q=&limit=` (cookie required) joins rooms and returns, newest first:

```json
{"results":[{"roomId":"…","roomName":"…","messageId":"…","role":"assistant",
  "createdAt":"…","snippet":"Baik Boss, <mark>NELAYANKAPAL</mark> sudah saya catat …"}]}
```

`limit` defaults to 20, caps at 50. Snippets come from `ts_headline` with the match wrapped in `<mark>`. The
browser escapes the whole snippet and restores only those two tags, so a message that literally contains
HTML cannot execute in the rooms panel. The search field sits above the room list; while typing, results
replace the list. A word nobody wrote gets the honest line “Tidak ada pesan yang cocok dengan …”, never a
blank list. Clicking a result goes to `/r/<roomId>?m=<messageId>`; the room scrolls that message into view
and rings it for 6 seconds.

Measured:

```
search: 2 hit(s) for NELAYANKAPAL, snippet=Baik Boss, <mark>NELAYANKAPAL</mark> sudah saya catat …
search: unseen term returned 0
```

## E. Export

`GET /api/rooms/<id>/export.md` (cookie required) returns `text/markdown; charset=utf-8`, `nosniff`, and
`Content-Disposition: attachment; filename="<room>-<YYYY-MM-DD>.md"`. The transcript contains the room name,
topic, export date, then each message with `**Kamu**` / `**Hermes**` / `**Sistem**`, its timestamp, the body
verbatim (fenced code blocks survive untouched), and an attachment list with names and Indonesian sizes.

Measured:

```
export: 443 bytes, 26 lines, attachment; filename=p5-pelayaran-2026-11-12.md
```

The room header’s `⋮` menu has “Ekspor transkrip — Markdown (.md)”. It downloads through the same cookie
(no signed URL), and the UI reads the filename from `Content-Disposition`.

## F. Tests and acceptance

- `go test ./...`: **78 tests in 8 packages**, including SPA fallback (`/r/x` HTML, `/api/nope` JSON 404,
  missing build keeps the API up), search (known phrase found, unseen ignored, 401 without a cookie), export
  (messages, code fence, attachment names, 401), and the opt-in restore parity test.
- `web`: **51 tests / 9 files**, `vue-tsc --noEmit` clean. New tests cover snippet escaping/highlighting and
  `Content-Disposition` filename parsing.
- `scripts/p5-acceptance.sh`: boots the real binaries + real voice sidecar on 3301/3310 against a throwaway
  Postgres, one real Hermes turn, export, search, backup, restore parity, then drops everything:

  ```
  P5 measured summary
    SPA shell:              1001 bytes at /
    hashed asset cache:     public, max-age=31536000, immutable
    /api/nope:              401 anonymous, 404 authenticated, JSON both
    Hermes turn:            6 s, 2 messages
    export:                 443 bytes, 26 lines
    search:                 2 hit(s), 0 for an unseen term
    backup:                 4404 bytes
    restore parity:         source [1|2|1|0] = restored [1|2|1|0]
  PASS: P5 hardening against the real stack
  ```

- `scripts/p5-browser.sh` (headless Chromium over CDP, no Playwright): one real seeded room, then at
  **390×844** and **1440×900** signs in, sees the empty state, searches a real word, opens the deep link,
  exports from the room menu and reads the downloaded file back. **12/12 checks passed**; downloads were
  508 bytes and contained both labels, the token and `bukti-p5.txt`. Screenshots:
  `changelogs/assets/p5-phone-search.png`, `p5-phone-room.png`, `p5-desktop-search.png`,
  `p5-desktop-room.png`; machine results in `changelogs/assets/p5-results.json`.

## G. HTTPS with Tailscale — exact commands, no sudo assumed

Run as your normal user. If `tailscale serve` says permission denied, the operator must grant operator
rights once (that one command needs sudo; it is the only one):

```bash
# once, only if `tailscale serve` is not permitted for your user:
sudo tailscale set --operator="$USER"

# HTTPS on 443 for this machine's tailnet name, proxied to loopback irisd:
tailscale serve --bg --https=443 http://127.0.0.1:3300
tailscale serve status
```

If your Tailscale build only has the older syntax:

```bash
tailscale serve https / http://127.0.0.1:3300
```

Then:

```bash
# enable secure cookies now that the origin is HTTPS
printf '\nIRIS_COOKIE_SECURE=true\n' >> ~/.config/iris/env   # or uncomment it by hand
systemctl --user restart iris.service
```

Open `https://<machine>.<tailnet>.ts.net` on the phone. The certificate is issued by Tailscale; no certbot,
no DNS change. Tailnet HTTPS must be enabled once in the Tailscale admin console (DNS page).

## H. What still needs a human

1. **Enable the units.** They are installed in `~/.config/systemd/user/` and disabled (`--no-start` was used
   on purpose): starting `iris.service` against the live `iris` database is a production write, and this
   session was told not to touch it. After reviewing `~/.config/iris/env`, run `./deploy/install.sh`.
2. **Migrate the live database** to 000007 before search works there:
   `~/go/bin/migrate -path db/migrations -database "$IRIS_DATABASE_URL" up`.
3. **Linger** (survive reboot with no login session): `loginctl enable-linger "$USER"`. Not run here — it
   changes a login-session policy, not a repo file.
4. **HTTPS**: the commands in §G, including the one-time Tailscale operator grant if needed.
5. **A real reboot** is the only remaining proof that the units come back; this session cannot reboot the
   machine and still show you the evidence.
6. **Backups on a schedule** are manual by design (no timer unit in this phase). Add a user timer if you
   want the 7-archive rotation to run without remembering `scripts/backup.sh`.

No commits, no push; the tree is intentionally dirty.

## Deployed and verified by the operator (live, public URL, real Hermes)

```
linger                 : yes (Boss ran loginctl enable-linger)
tailscale serve        : https://server-001.tail4ed9c7.ts.net:9443 → 127.0.0.1:3300 (Boss ran it)
units                  : iris.service active, iris-voice.service active
public URL             : 200, <title>Iris</title>
deep link /r/anything  : 200 (SPA history fallback)
/api/nope              : {"error":"Sign in to Iris first."} — JSON, never HTML
secure context         : window.isSecureContext true, navigator.mediaDevices present (mic will work)
turn over the public URL: 150 SSE lines, event: done
search                 : "oranye" → 2 results with <mark>oranye</mark>; "zebrakuning" → 0 results
export                 : 631 bytes, 12 lines, filename p5-live-check-2026-11-12.md
backup                 : 4 396 bytes, 7 entries, sha256 recorded, rotation keeps 7
restore into scratch   : messages live=2 restored=2 PARITY, content identical
guards                 : live db refused without --force, postgres refused
crash resilience       : kill -9 on the MainPID → new PID within 4 s, status active, health 200
78 go tests, 12 packages
```

## Two defects found by the operator

1. **A scratch restore merged room files into the LIVE folder.** `restore.sh iris_restore_check` wrote
   attachments to `~/iris-data/rooms` — the directory the running service uses — because the data dir default
   did not depend on the target database. Two databases' files would mix on disk while the live one was
   running. Now only the live target may write to the live data dir; any other target gets
   `~/iris-data.<target>` unless `--data-dir` is explicit. Verified: live folder file count 1 → 1, untouched.
2. **Backups were manual**, so the one thing that matters after a disaster was the one thing nothing
   scheduled. Added `iris-backup.service` + `.timer` (daily 03:30, `Persistent=true`, 10-minute jitter),
   enabled; a manual run produced a fresh archive and the unit finished cleanly.

The restore guards themselves were correct: they refused the live database without `--force` and refused
`postgres`. My first two attempts failed because I invoked the script wrongly, not because the script was wrong.
