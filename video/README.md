# Product videos

Two videos made entirely from code: no screen recording, no editor, no
recorded audio.

| Film | What it shows | Output |
|---|---|---|
| `intro` | What Applysmith is and its main features | `video/dist/applysmith-intro.mp4` |
| `setup` | Install, the setup steps and the first use | `video/dist/applysmith-setup.mp4` |

Make both from the repository root:

    make videos

That captures the screenshots, then renders. The two steps on their own, and
one film alone:

    go run ./cmd/video shots
    go run ./cmd/video render setup

It needs Google Chrome and `ffmpeg` (with libx264) on this computer. The output
is 1920x1080 at 30 frames a second, H.264 with AAC audio. `video/shots/` and
`video/dist/` are ignored by git: both are made from code. A render takes about
three seconds for each second of film.

The screenshots stay out of git because they would take most of what is left
of the repository byte budget (`internal/guard/budgets.go`). Run one render at
a time: two renders write the same files in `video/dist/` and leave a damaged
video.

## How it works

| Part | Where |
|---|---|
| The scenes of both films: order, length, every word on screen | `films.js` |
| The player that draws a scene for a given time | `player.html`, `player.js`, `player.css` |
| The renderer and the music | `cmd/video` |
| Screenshots of the demo workspace, made by `shots` | `shots/` |

The player exposes `seek(t)`, and every frame is a function of `t` alone. The
renderer opens the player in a headless Chrome, asks for each frame in turn,
takes a screenshot and pipes it to `ffmpeg`. To look at a film without
rendering, serve the repository root over HTTP, open
`video/player.html?film=intro` and call `seek(12)` in the console.

The music is synthesized by `cmd/video/music.go`: a four-bar chord progression
with a pad, an arpeggio, a bass, soft drums and a bell, written to exactly the
length of the film and faded at both ends. Each film has its own score (tempo,
chords, drum level) in the `scores` table. `go run ./cmd/video music intro`
writes only `video/dist/intro.wav`, to listen to a change quickly.

## Changing a film

Edit `films.js`. The words must stay true to `README.md`, `docs/setup` and
`docs/user-guide`: check a claim there before writing it on screen. Scene kinds
are `title`, `cards`, `shot`, `terminal` and `steps`; `scroll` on a `shot` is
the vertical travel of the screenshot, in CSS pixels of the capture.

## Screenshots

Every screenshot is of a demo workspace, never a real one. `shots` builds the
program, creates a demo workspace in a temporary folder and walks
[the tutorial](../docs/user-guide/tutorial.md) there: it captures the inbox,
chooses the Offline provider, evaluates the three sample postings, runs a dry
run of the sample form (the apply window opens for a few seconds) and captures
the other pages. The demo server listens on a free port, so a running
Applysmith is left alone. A dashboard without the demo banner is refused.
