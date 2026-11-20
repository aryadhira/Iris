# Room media playback

**Date:** 2026-11-15 (measured 2026-11-20)
**Scope:** assistant artifacts, room file panel, lightbox, upload allowlist, inline disposition.

## What changed

Hermes already shared files, but only the user side could play them: an assistant image/video/sound
arrived as an `ArtifactCard` download link. Sound had no player anywhere, the allowlist rejected an
uploaded mp3, and `/api/files/:id` served every non-image/non-PDF as `attachment`, so even a shared clip
could not stream.

- **Assistant artifacts** split by kind. Images are thumbnail buttons that open `FileLightbox`; video
  renders a `<video controls playsinline preload="metadata">` capped at `max-h-72`; sound renders a
  compact `<audio controls preload="metadata">` row. Documents keep `ArtifactCard`. Every media item
  keeps a download control.
- **Room file panel** shows a thumbnail for images (opens the lightbox), inline players for video and
  sound, and the existing download/delete controls (delete confirmation untouched).
- **`FileLightbox`** handles image, video and sound, closes on Escape or a backdrop click, and pauses
  its video before disappearing.
- **Backend allowlist** gains `audio/mpeg`, `audio/mp4` (M4A/M4B brands), `audio/ogg`, `audio/wav`,
  `audio/webm`, `audio/flac`, `audio/aac`. `files.IsMedia` gives sound the same 64 MB ceiling as video;
  `IsVideo` still works and `IsAudio` is new. `Inline()` is true for `video/*` and `audio/*`.
- **Download intent** is explicit: media streams from `/api/files/:id`, while the download control uses
  the same URL with `?download=1` to force `Content-Disposition: attachment`.
- **Frontend helper** `web/src/lib/files.ts` gains `video`/`audio` kinds, `isAudio()`, `isMedia()`,
  `fileDownloadUrl()`, and extension-aware labels. `ArtifactCard` still handles documents.

## Measured evidence

### Go

`go test ./...` on the throwaway machine:

```
ok   github.com/aryadhira/iris/internal/files
ok   github.com/aryadhira/iris/internal/httpapi
... all packages ok
```

New unit coverage: mp3 (ID3 and raw frame), M4A ftyp brand, Ogg, WAV, FLAC, AAC, WebM audio naming,
`Allowed`, `Inline`, `IsMedia`/`IsVideo`/`IsAudio`, an M4A that must not become `video/mp4`, and a handler
test proving audio streams inline on a Range request and downloads as an attachment with `?download=1`.

### Web

```
Test Files  10 passed (10)
     Tests  77 passed (77)

vue-tsc --noEmit   (no output)

vite v8.3.8 building client environment for production...
✓ 1608 modules transformed.
dist/assets/index-CLFTC_uO.css   43.24 kB │ gzip:   8.65 kB
dist/assets/index-D-DVYYWB.js   378.96 kB │ gzip: 109.66 kB
✓ built in 777ms
```

### Throwaway instance

`scripts/p13-browser.sh`: DB `iris_p13_media` on `ailab-postgres`, `irisd` on **3316**, temp
`IRIS_DATA_DIR`, production `web/dist`, dummy Hermes URL `http://127.0.0.1:9` and a throwaway key. The
script drops the DB, kills the process and removes the temp tree on exit; `:3316` was free afterwards.

Real files generated with ffmpeg: a 640×360 PNG, a 2 s 440 Hz MP3, a 2 s H.264/AAC MP4, and a small PDF.
One assistant message owns all four rows.

Real response headers for the mp3:

```
HTTP/1.1 206 Partial Content
Accept-Ranges: bytes
Content-Disposition: inline; filename=lagu-uji.mp3
Content-Length: 1024
Content-Range: bytes 0-1023/24808
Content-Type: audio/mpeg
X-Content-Type-Options: nosniff
```

The download control (`?download=1`):

```
HTTP/1.1 200 OK
Accept-Ranges: bytes
Content-Disposition: attachment; filename=lagu-uji.mp3
Content-Length: 24808
Content-Type: audio/mpeg
X-Content-Type-Options: nosniff
```

Chromium at 390×844 and 1440×900:

```
[PASS] 390: assistant message has an <audio controls> player for the mp3
[PASS] 390: audio does not autoplay or loop and preloads metadata
[PASS] 390: assistant message has a <video controls> player for the mp4
[PASS] 390: video is inline, muted by policy (no autoplay), and preloads metadata
[PASS] 390: video height is capped around max-h-72 — {"width":318,"height":179}
[PASS] 390: image artifact is a thumbnail button
[PASS] 390: thumbnail loads the png from /api/files/...
[PASS] 390: the pdf keeps the document artifact card
[PASS] 390: clicking the thumbnail opens the lightbox with the image — naturalWidth=640
[PASS] 390: Escape closes the lightbox
[PASS] 390: backdrop click closes the lightbox
[PASS] 390: clicking download saves the mp3 — {"name":"lagu-uji.mp3","size":24808}
[PASS] 390: the download URL answers Content-Disposition: attachment
[PASS] 390: mp3 Range request answers 206
[PASS] 390: mp3 Range request streams inline
[PASS] 390: mp3 Range request advertises Accept-Ranges: bytes
[PASS] 390: mp3 Range request returns exactly 1024 bytes
[PASS] 390: the room file panel has an audio player
[PASS] 390: the room file panel has a video player
[PASS] 390: the room file panel image opens the lightbox
[PASS] 390: delete and download controls survive in the file panel
[PASS] 1440: audio/video/thumbnail/card assertions repeat
[PASS] all: no console errors or uncaught exceptions — []
```

DOM snapshot at 390 (one assistant message):

```json
{"audio":{"present":true,"preload":"metadata","autoplay":false,"loop":false,"controls":true,"box":{"width":302,"height":54}},
 "video":{"present":true,"preload":"metadata","playsinline":true,"autoplay":false,"loop":false,"controls":true,"box":{"width":318,"height":179}},
 "thumbnail":{"tag":"BUTTON","src":"/api/files/33333333-3333-4333-8333-333333333301"},
 "documentCard":{"testid":"artifact-card","download":"catatan.pdf"},
 "audioDownload":"/api/files/33333333-3333-4333-8333-333333333302?download=1",
 "videoDownload":"/api/files/33333333-3333-4333-8333-333333333303?download=1"}
```

Screenshots:

- `changelogs/assets/p13-390-media.png`
- `changelogs/assets/p13-1440-media.png`
- `changelogs/assets/p13-390-lightbox.png`
- `changelogs/assets/p13-1440-lightbox.png`

Machine-readable results: `changelogs/assets/p13-results.json`.

## Still missing / honest limits

- **No real phone measurement.** 390×844 and 1440×900 are Chromium viewports. iOS Safari, the installed
  PWA and Android Chrome remain Boss' measurements. Codec support is the browser's: an MP4 whose H.264
  profile the browser cannot decode will show a player that does not play, not a broken layout.
- **WebM is ambiguous.** A sound-only `.webm` with no `.weba` name and no early `A_OPUS`/`A_VORBIS`
  marker is still classified `video/webm`; it renders in the video player because the container is shared.
- **The lightbox has no video/audio entry point from the transcript.** Assistant video and sound play
  inline, and `FileLightbox` supports both, but today only image thumbnails open the lightbox. The room
  file panel also keeps its video/audio players inline.
- **No poster frame** for video; the first frame appears when metadata lands inside the fixed-aspect
  wrapper (so the bubble does not move), not before.
- **Download attribute on iOS** may open the media instead of saving it, because the platform owns that
  decision. The server still answers `Content-Disposition: attachment` on `?download=1`.
