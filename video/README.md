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

## The site film

The 37-second film on the website (`website/README.md`, "The film") is not
made by `cmd/video`. It is an OpenMotion project ("Applysmith site film", titled in Chinese),
kept in the OpenMotion workspace on the owner's Mac; version 8 is the one on
the site. It tells the page's own story with the page's paper sheets: postings
scatter, form a stream, are scored in a wall, the skipped ones fall away, one
posting gets a résumé and a cover letter, the form is filled and stops before
Submit, and the pack is sealed. The sheets, the scores and the form are drawn;
the only real screen is the dry-run panel cropped from
`docs/user-guide/images/tutorial-06-sample-form-filled.webp`. Every word on
screen comes from the website.

Its music is the `intro` score of `cmd/video/music.go` at exactly 36.923 s
(16 bars at 104 BPM), written by calling `compose` and `writeWAV` from a
throwaway `main` (the `music` command takes its length from a film in
`films.js`). The cuts fall on bar lines: the wall lands as the drums enter
(bar 4, 9.23 s) and the seal lands where the music thins (bar 13, 30.0 s).

The web files are made from the OpenMotion export and are not in git:

    ffmpeg -i export.mp4 -c:v libx264 -preset slow -crf 23 -profile:v high \
      -pix_fmt yuv420p -movflags +faststart -c:a aac -b:a 128k video/dist/site-film.mp4
    ffmpeg -ss 2.6 -i export.mp4 -frames:v 1 -vf scale=1280:-1 -q:v 4 video/dist/site-film.jpg
