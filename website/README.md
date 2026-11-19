# Website

The public page for Applysmith: one page, with a three.js scene behind it.
A few hundred sheets of paper follow the scroll position: scattered postings
become a stream, the stream is sorted, the chosen sheets are stacked and the
stack is sealed.

    bun install
    bun run dev       # local server with reload
    bun run build     # writes dist/ (ignored by git)
    bun run preview   # serves dist/ on port 4173

| Part | Where |
|---|---|
| The words and the structure of the page | `index.html` |
| Colors, type and layout | `src/style.css` |
| The scene: arrangements, lights, camera per section | `src/scene.js` |
| Brand mark | `../web/src/assets/career-cat.webp`, shared with the dashboard |
| The film | `../video/dist/site-film.mp4` and `site-film.jpg`, not in git (below) |
| The four screens | `../docs/user-guide/images/tutorial-0{4,5,6,7}-*.webp`, shared with the tutorial |

Every claim about Applysmith comes from `README.md`, `docs/setup` and
`docs/user-guide`; check a claim there before changing the words. The page
links to the source at `https://github.com/Jaydccq/Applysmith` and to no download, because
no public one exists yet.

## The screens and the questions

"See it at work" shows four screenshots of the demo workspace, one at a time,
chosen by a tab or the arrow keys. They are the tutorial's own images, imported
from `docs/user-guide/images` in `src/main.js`, so the site adds no image to
the repository; when the tutorial's screenshots are retaken the site shows the
new ones at its next build. Each must show the demo workspace only.

## The film

"The whole idea, in 37 seconds." sits above the screens. Its files are far
over the one-mebibyte limit for a tracked file, and the repository's byte
budget has almost no room left (`internal/guard/budgets.go`), so they stay
out of git. `src/main.js` picks up `video/dist/site-film.mp4` and
`site-film.jpg` when they exist and Vite serves them from `/assets/` like
every other file, so the page still calls no other host. Without them the
build passes and the section stays hidden. How the film is made:
[video/README.md](../video/README.md), "The site film". The two product videos
made by `make videos` are not on the page.

Each answer under "Questions" restates `README.md`, `docs/setup/00-prerequisites.md`
or `docs/setup/09-llm.md` (the providers, the Offline choice and the default
spend cap).

## The comparison table

Each cell about another tool comes from that tool's own pages, checked on
2026-11-16; a dash means its pages do not say. Recheck the pages and the date
in the page's footnote before changing a cell. Massive is left
out: the app's company could not be confirmed (usemassive.com
describes an expert marketplace, not the app). Teal's site refuses automated
reads, so its home page was read in a browser.

| Tool | Pages |
|---|---|
| career-ops | github.com/career-ops-hq/career-ops (README) |
| Simplify | help.simplify.jobs/articles/5623502-whats-included-in-simplify-features-and-pricing |
| Jobright | jobright.ai |
| JobCopilot | jobcopilot.com, its Chrome Web Store listing |
| LoopCV | loopcv.pro/pricing |
| LazyApply | lazyapply.com, lazyapply.com/pricing |
| Huntr | huntr.co, huntr.co/pricing |
| AIApply | aiapply.co/product, aiapply.co/pricing |
| Teal | tealhq.com (in a browser), its Chrome Web Store listing |

## Address, search and link previews

The site lives at `https://www.ovreni.org/`. That address is written in four
places, which change together: the canonical and `og:` tags in `index.html`,
`public/robots.txt` and `public/sitemap.xml`. `public/og.jpg` (1200 by 630) is
the link-preview image: the opening screen at that size, taken from
`bun run preview` with headless Chrome and saved as JPEG.

`ovreni.org` without `www` is redirected at Cloudflare, not in the site: two
Single Redirect rules on the zone (Rules, Redirect Rules), each a 301 that
keeps the path and the query string. `https://ovreni.org/*` goes to
`https://www.ovreni.org/${1}` and `http://ovreni.org/*` to the same address
(a pattern with a wildcard scheme is read as a host named `http` and does not
match). Check with `curl -sI http://ovreni.org/robots.txt`.
The zone's "Always Use HTTPS" setting (SSL/TLS, Edge Certificates) is on, so
`http://www.ovreni.org/` moves to https as well.

Google Search Console has `ovreni.org` as a domain property, verified by a
`google-site-verification=…` TXT record on the zone apex. Removing that record
drops the verification. The sitemap `https://www.ovreni.org/sitemap.xml` is
submitted there (Indexing, Sitemaps), so a new page only needs a `<url>` entry
in `public/sitemap.xml`.

`public/favicon.png` (96 px) and `public/apple-touch-icon.png` (180 px) are
the brand mark, written by `sips -s format png -z <size> <size>` from
`../web/src/assets/career-cat.webp`.

## How the scene follows the page

Each section carries `data-scene="<n>"`. `stages` in `src/scene.js` gives, for
each number, the arrangement of the sheets, where the group sits, its tilt and
the camera distance. The scene blends between the stages of the two sections
nearest the middle of the viewport. All text is in the page itself, not in the
canvas, so the page reads the same without WebGL; with reduced motion the
scene still follows the scroll but nothing drifts.

three.js and both typefaces (Fraunces, Instrument Sans) are bundled from npm
and served from the site itself; the page calls no other host.

## Publishing

The site is static: `dist/` is served as the assets of a Cloudflare Worker.
The Worker's name, the account and the token are given at the command line or
in the environment, never in this repository. Its only code, `worker.js`,
runs first for `/assets/*.mp4`: static assets answer a byte-range request
with the whole file, and Safari plays a video only from ranged (206)
responses. Check with
`curl -sI -H "Range: bytes=0-1" https://www.ovreni.org/assets/site-film-<hash>.mp4`.

    bun run build
    bunx wrangler versions upload --name <worker>
    bunx wrangler versions deploy --name <worker>

`wrangler.jsonc` holds the rest: the compatibility date, `dist/` as the
assets, and `public/404.html` for any address that is not a file.
`public/_headers` lets browsers keep the hashed files under `/assets/`
for a year.

Before uploading, check that `dist/assets` holds a `site-film-*.mp4`: a
build without the film publishes the page without it.

`versions upload` makes a version with a preview address and leaves the live
site alone; `versions deploy` makes a version live, and the same command with
an earlier version puts the earlier page back.
