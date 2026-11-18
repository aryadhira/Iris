# Iris on the public internet: iris.aryalogi.my.id

Boss bought aryalogi.my.id and asked for Iris to be reachable without Tailscale, through Cloudflare.

## What was built

- `cloudflared tunnel create iris` → `6b2c1f47-…`, kept **separate** from the existing `server_001` tunnel so
  the two never share a config or a failure mode.
- `cloudflared tunnel route dns iris iris.aryalogi.my.id` → CNAME to `6b2c1f47-….cfargotunnel.com`.
- `~/.cloudflared/iris.yml` (mode 600): one ingress — `iris.aryalogi.my.id → http://127.0.0.1:3300`, everything
  else `404`. The tunnel cannot reach any other local service, by construction.
- `iris-tunnel.service` as a **user** unit (no sudo), `Restart=always`, `After=iris.service`, enabled in
  `default.target.wants` — it comes back after a reboot with the rest.

## Security work this exposed (the real change)

Iris was designed for a tailnet: one passphrase, no user table, no throttling. A public hostname turns that into
an internet-speed guessing game, so:

- **Login lockout**: 5 failures per client per 15 minutes → `429` with an Indonesian message and `Retry-After`.
  Per client, so one attacker cannot lock Boss out.
- **Trusted client identity**: `CF-Connecting-IP` is honoured only when the socket itself is loopback (i.e. the
  request really came through cloudflared); otherwise the socket address wins, because the header is forgeable
  by anyone who can reach port 3300 directly.
- **Secure cookies** on (`IRIS_COOKIE_SECURE=true`).

Tests: blocks after five failures and recovers after the window; a successful sign-in forgives past failures; a
different client is unaffected; the forwarded header is ignored from a non-loopback peer.

## Verified through the public hostname (not localhost)

```
GET /                        200  server: cloudflare  cf-ray present
<title>                      Iris
GET /api/health              200
POST /api/auth/login         200, Set-Cookie: HttpOnly; Secure; SameSite=Lax
GET /api/rooms (with cookie) 200
POST login (wrong phrase)    401
SSE stream                   1.16s event:run → 6.56s event:delta → 6.74s event:done
                             (not buffered by the edge: deltas arrive as they happen)
```

## Left to Boss (deliberately)

**Cloudflare Access** on this hostname — an email one-time code in front of the passphrase. Iris can run shell
commands on this machine; a single password on a public URL is one leak away from that being someone else's
machine. Two minutes in Zero Trust → Access → Applications, and I cannot do it for you because it needs your
Cloudflare account.

Also unchanged: `cloudflared` on this host is 2026.10.0 and warns that 2026.11.1 exists. Not urgent, worth a
maintenance pass.
