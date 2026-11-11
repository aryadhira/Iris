# Iris web shell

Vue 3 + Vite + TypeScript + Tailwind v4. Hand-built components, no component
library. The browser only talks to irisd; the Hermes key never leaves the
server.

## Run it

```bash
cd web
npm install
npm run dev            # http://127.0.0.1:5175, proxies /api to :3300
```

`irisd` must already be running on `127.0.0.1:3300`. To point the proxy
somewhere else (the acceptance run uses `:3301`):

```bash
IRIS_API_TARGET=http://127.0.0.1:3301 npm run dev
```

Production-style check:

```bash
npm run build          # vue-tsc --noEmit + vite build
npm run preview        # http://127.0.0.1:4175, same proxy
```

Tests:

```bash
npm test               # vitest: SSE parser + room-store merge rules
npm run typecheck
```

## Shape

```
src/lib/sse.ts        SSE frame parser (chunk boundaries, multi-line data)
src/lib/merge.ts      stored rows ↔ what is already on screen (no duplicates)
src/lib/api.ts        the irisd JSON surface
src/stores/room.ts    one room: load, stream, reconnect, draft, error
src/stores/rooms.ts   room list + last-room memory
src/views/            Login, AppFrame (sidebar/sheet), Rooms, Room
src/components/       composer, message, working line, room list, dialogs
```

Styling lives in `src/style.css`. Brand tokens come straight from `BRAND.md`;
gradients are used only for the login halo.

## Fonts and icons

Inter Tight is self-hosted from `@fontsource-variable/inter-tight` and bundled
by Vite — no CDN at runtime.

Icons are derived from `assets/brand/iris-master.jpg` (never redrawn):

| File | Derivation |
|---|---|
| `icon-512.png`, `icon-192.png` | master resized, rounded-square area kept, corners transparent |
| `apple-touch-icon.png` | the same, flattened on `#080321` (iOS ignores alpha) |
| `icon-maskable-512.png` | master at 80 % inside a 512 `#080321` field |
| `mark-16.png`, `mark-32.png`, `favicon.ico` | 360×360 crop at `+446+337`, circular mask, resized — the mic in the teardrop only |

Regenerate the small mark with:

```bash
convert assets/brand/iris-master.jpg -crop 360x360+446+337 +repage \
  \( -size 360x360 xc:black -fill white -draw "circle 180,180 180,2" \) \
  -alpha off -compose CopyOpacity -composite /tmp/iris-mark-circle.png
convert /tmp/iris-mark-circle.png -resize 32x32 web/public/icons/mark-32.png
convert /tmp/iris-mark-circle.png -resize 16x16 web/public/icons/mark-16.png
convert web/public/icons/mark-16.png web/public/icons/mark-32.png web/public/favicon.ico
```
