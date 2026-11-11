# Iris — brand

**Name:** Iris. Greek goddess of the rainbow and messenger of the gods; the iris of an eye;
the iris flower the mark is drawn as. One word covers voice, sight and delivery.

**Tagline:** your Hermes, in your own rooms.

**Master asset:** `assets/brand/iris-master.jpg` (1254×1254, Boss's mark — do not redraw it).

## Palette, sampled from the mark

| Token | Hex | Where it comes from |
|---|---|---|
| `bg` | `#080321` | the deep indigo the icon sits on |
| `bg-deep` | `#05031C` | corners, vignette |
| `surface` | `#0E0729` | panels (bg lifted, never grey) |
| `line` | `#241A4D` | hairlines between panels |
| `violet` | `#5218DF` | the left petal — primary action |
| `orchid` | `#C675FE` | the halo behind the mic — focus, active state |
| `blue` | `#2431DF` | the right petal — secondary/info |
| `ink` | `#F7F1FF` | the mic and robots — body text |
| `muted` | `#A99FD0` | derived: 60% ink on bg |
| `quiet` | `#6F66A0` | derived: labels, timestamps |

Gradients are allowed only where the mark uses them: the petals (violet→magenta, blue→cyan)
and the halo. Everything else is flat.

## Icon set

One master is not enough, and pretending otherwise is how products end up with a mush
favicon. The mark carries three robots, an arch, a mic, a bubble and two gradients — at
32 px the robots become specks and at 16 px it reads as "a purple blur".

| Size | What to use |
|---|---|
| 512, 192, apple-touch | the full master, uncropped |
| 48 | full master, simplified gradients |
| 32, 16, favicon.ico | **a reduced mark**: the white mic inside the violet teardrop, no robots, no arch, no bubble, no gradient |
| maskable PWA | full master, with the safe zone (80%) respected |

The reduced mark is derived from the master, never hand-drawn separately as a different
idea — it is the same silhouette with the small parts removed.

## Voice

Spoken replies are a summary, never the formatted text: markdown stripped, code and tables
dropped, numbers expanded, two sentences maximum, full answer on screen. Rate `-8%`.
Indonesian female (`id-ID-GadisNeural`) unless Boss says otherwise.
