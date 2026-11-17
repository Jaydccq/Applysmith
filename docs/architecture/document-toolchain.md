# Document toolchain

Task 3.6a proves and pins the tools behind new résumé and cover-letter PDFs
(design D19, [document-toolchain spec](../../openspec/changes/migrate-to-go-mygo/specs/document-toolchain/spec.md)).
Everything below was measured on macOS 26 (Darwin 25.2.0, arm64) on 2026-10-11
with Go 1.27.1. Packages: `internal/documents/tex` (encoders),
`internal/documents/assets` (embedded templates, class, scripts, fonts),
`internal/documents/toolchain` (detection, setup, smoke, verifier contract).

## What is pinned

| Component | Pinned | How it is held |
|---|---|---|
| LuaHBTeX (`lualatex`) | 1.21.0, TeX Live 2025, format 2025.3.8 | detected version, part of the smoke key |
| XeTeX (`xelatex`) | 3.141592653-2.6-0.999997, TeX Live 2025 | same |
| kpathsea | 6.4.1 | found next to the engines |
| moderncv | 2.4.1 (2024-07-18), minimum enforced | `ProvidesClass` line read from the file |
| TeX closure | 64 witnesses, 67 packages read (table below) | `toolchain/lock.json`, one witness file per package |
| Python | 3.11 or newer; verifier scripts run on 3.11.14, 3.13.11 and 3.14.7 | `lock.json` |
| pypdf | 6.19.0, wheel `7e5d6e730e7dae87d560a2cee218b852f6498c8be61966f3cd02ead971e48d14` (released 2026-09-16) | `assets/requirements.txt`, installed with `--require-hashes --only-binary=:all: --no-deps` |
| pip (venv bootstrap) | 26.2.1 | bundled by `ensurepip`, never upgraded |
| Poppler | 26.10.0 (`pdftotext`, `pdftoppm`); 26.05 is the enforced minimum because, per the upstream script's note, earlier 26.0x aborts `-bbox` on a PDF with an empty Info string (not reproduced here) | `pdftotext -v` |
| Lato | 2.015, six unmodified TrueType files | embedded, hash in the manifest |
| Raleway | 4.101, two unmodified OpenType files | embedded, hash in the manifest |

The font files are stored compressed by `zopfli --gzip`
(`fonts/lato/Lato-Bold.ttf.gz`, ...) to keep the repository small;
`assets.FS()`, `assets.Read` and `assets.FontFiles` serve them decompressed
under the real names, and every hash below and in the manifest is of the real
font bytes (the guard gunzips a `.gz` destination before hashing). To add or
update a font, compress the upstream file with `zopfli --gzip` (gzip format
with the header `gzip -9 -n` writes, about 6% smaller), keep the `.gz`
destination in `third_party/document-sources.json`, and keep the real file name
in `NOTICE`.

Python is the Homebrew `python@3.14` 3.14.7; the venv holds a symlink to it, so
`brew upgrade python` can break the venv. The detector then reports
`python-venv` as unsupported and `applysmith documents toolchain setup` rebuilds
it.

pypdf 6.19.0 was chosen over the newer 6.20.0 (released two days before the
proof) to avoid pinning a release that has had no time in the field. It needs
no dependency on Python 3.11 or newer, which is why 3.11 is the floor.

## Where things live

The venv, a private TeX cache, the extracted requirements and the smoke record
live in one per-user directory, `<user config dir>/applysmith/documents-toolchain/`
(on macOS `~/Library/Application Support/applysmith/documents-toolchain/`), mode
0700. The spec calls the toolchain per-user installation state and does not
name a location, so it is not inside a workspace: a venv and a TeX cache would
otherwise be copied per workspace and could reach backups and diagnostic
exports. `APPLYSMITH_DOCUMENT_TOOL_PATH` (a list of directories) replaces the
executable search; it is machine configuration, never request input.

Executables are found in that list, else in `PATH` followed by
`/Library/TeX/texbin`, `/usr/local/texlive/bin`, `/opt/homebrew/bin`,
`/usr/local/bin` and `/usr/bin`, because an app started from the Finder has a
short `PATH`. Each is resolved once to an absolute path.

## Exact commands

Every tool runs through one runner: absolute executable, argument vector (no
shell), an environment of exactly these variables and nothing inherited,
`PATH=<engine dir>:/usr/bin:/bin`, `HOME=<root>/home`,
`TEXMFHOME=<private HOME>/texmf`, `TEXMFVAR=<root>/texmf-var`,
`TEXMFCONFIG=<root>/texmf-config`, `LC_ALL=C`, `PYTHONNOUSERSITE=1`,
`PYTHONDONTWRITEBYTECODE=1`. The child is the leader of its own process group;
timeout, cancellation and excess output send the group SIGTERM, then SIGKILL
after two seconds, and the runner sweeps the group once more before returning.
Limits: 60 s per engine, 30 s per helper, 1 MiB of retained output per stream,
10 MiB per PDF, 100 MiB of scratch per run. A build removes its scratch
directory when it ends; one left by a crashed process is removed by the next
build once it is an hour old.

| Step | Command (working directory is the private build directory for engines) |
|---|---|
| Résumé | `<texbin>/lualatex -no-shell-escape -interaction=nonstopmode -halt-on-error -file-line-error -no-parse-first-line -recorder resume-a4.tex`, a second pass only when the log asks for a rerun |
| Cover letter | the same vector with `xelatex` and `cover.cls` plus `fonts/lato`, `fonts/raleway` copied beside the source |
| Text layer and checks | `<venv>/bin/python -I - <pdf> [--pages N] [--max-pages N] [--min-chars N] [--contains TEXT]... [--ascii-dates] [--dump-text PATH]` with `verify_pdf.py` on standard input |
| Layout | `<venv>/bin/python -I - <pdf> --pdftotext <abs path> --template resume\|cover --paper a4\|letter` with `verify_layout.py` on standard input; the script runs `pdftotext -bbox -enc UTF-8 <private copy> -` and `pdftotext -v` |
| Raster previews | `pdftoppm -r 40 -png <pdf> <prefix>-page` |
| Poppler text | `pdftotext -enc UTF-8 <pdf> -` |
| Package locate | `kpsewhich -- moderncv.cls <witness>...` |
| Venv | `<python> -I -m venv <root>/venv`, then `<venv>/bin/python -I -m pip install --require-hashes --only-binary=:all: --no-deps --no-cache-dir -r <root>/requirements.txt` with `PIP_CONFIG_FILE=/dev/null`, `PIP_DISABLE_PIP_VERSION_CHECK=1`, `PIP_NO_INPUT=1` (and the proxy and CA variables when set) |

The scripts are fed on standard input under `python -I`, so no interpreter
loads a module from a directory that holds downloaded or generated files, and
no script is written to disk.

`-no-shell-escape` is not an OS sandbox. A paranoid read policy
(`-cnf-line=openin_any=p`) was tried and rejected: luaotfload then fails to load
its `multiscript` module and LuaLaTeX cannot start.

## Verifier contract (schema_version 1)

Both scripts take the PDF path and the expectations on the command line and
print one JSON object; exit 0 means a complete result was written, exit 2 is a
usage error and exit 3 an internal error, both without a result. Fields common
to both: `schema_version`, `tool`, `script_version`, `pdf_sha256`, `pdf_bytes`,
`python`, `checks[]` (`id`, `status` of `pass`, `fail` or `unverified`,
`reason`) and `overall`. The scripts read the PDF once; `pdf_sha256` is the hash
of those bytes. Both scripts are at `script_version` 2 since task 3.7a.

- `verify_pdf` adds `pypdf`, `extractor` (always `pypdf`), `pages`,
  `text_sha256`, `font_usage` (letters per font and size, or null). Checks in order: `pdf-readable`, `text-layer`, then only the
  requested ones of `page-count`, `max-pages`, `required-text`, `ascii-dates`.
  Required text is reported by index, never quoted. Without pypdf every
  requested check is `unverified`.
- `verify_layout` adds `profile`, `poppler`, `pages`, `measurements[]` per
  page. Checks: `page-text`, `page-size`, `clipping`, `text-overlap`,
  `internal-hole`, `early-page-end`, `footer-collision`, `final-page-thin`,
  `orphaned-entry`, and `entry-gap` for the résumé profile only. A missing, xpdf
  or aborting `pdftotext`, or one that returns no page, makes all of them
  `unverified`. The thresholds are one profile per template and paper size.

Go (`toolchain.ParsePDFResult`, `ParseLayoutResult`) decodes strictly (unknown
fields and trailing data refused), requires the schema version and tool name,
the exact list of requested checks in order (for the layout script, the list of
the requested profile, which the result must echo), a status from the enum, an
`overall` that follows from the checks, and `pdf_sha256` equal to the hash Go
computed. `VerifyPDF` and `VerifyLayout` hash the file before and after the run
and refuse a file that changed in between. A check that could not run is
`unverified`, never a pass. A `reason` may quote a few words of the document, so
it stays in private diagnostics.

## Readiness and the smoke

`applysmith documents toolchain check [--json]` and the `resumes.toolchain` doctor
leaf (`applysmith doctor --only resumes`) only read. States: `unavailable`
(error code `document-toolchain-unavailable` with the exact missing components),
`verification-needed` (everything present, no valid smoke) and `ready`. A smoke
is valid for one key: SHA-256 over the lock, the hash of every embedded file,
each component's name, path and version, and the size and modification time of
each executable and TeX witness file. An engine, Poppler, moderncv, venv, lock,
template or font change gives a new key. The record lives in
`<root>/smoke/<key>.json`, one file per key (the newest 8 are kept), so a build
with other templates, or a tool change, needs its own smoke and never stales the
record of another build that shares the root.

`applysmith documents toolchain smoke`, `setup --yes` and `POST
/api/documents/toolchain/smoke` (Setup's "Run the render test", offered in
`verification-needed`, and the same button in the Documents drawer after a
refusal that names the render test) are the only writers; the route refuses, writing
nothing, while a component is missing, and answers with the new report.
`setup` first prints the download and disk budget and exits 3 without `--yes`;
it creates the venv and then runs the smoke, and it never installs TeX or
Poppler. The smoke renders both templates on A4 and Letter from fictional
Alex Example data containing `& % $ # _ {} ~ ^ \`, `[` at the start of a
bullet, a double hyphen and an accented word, compiles them, runs both
verifiers, rasterises previews, extracts text with Poppler and writes
`smoke/<key>.json`, with `passed` false when any of the four documents fails. The leaf is never required: a
valid uploaded résumé finishes the résumé step while rendering is unavailable.

## Cold private-root recheck, 2026-10-16

At source `1469dee4bde9`, `TestToolchain_RealSetupAndSmoke` passed uncached
with `APPLYSMITH_TEST_DOCUMENT_TOOLS=1` and a newly absent private toolchain root
(`/private/tmp/aj-cold-documents-_36xkhnr/toolchain`). Setup created a separate
Python venv from the hash-locked requirements and a cold TeX cache. All four
synthetic documents (resume and cover letter, A4 and Letter) compiled with
two passes and passed both text and layout verification. The test took
16.62 seconds; the package took 16.995 seconds. The persisted smoke record
has schema 1, `passed:true`, four document hashes and current readiness.
The external engines and Python were already installed on this account;
this does not prove a new macOS-user installation or Gatekeeper first launch.

## Findings

- A fresh user needs about 33 s for `setup --yes` (venv, pip, a cold
  luaotfload font database of about 8 s, four compiles). A warm compile is
  under one second.
- LuaTeX applies ligatures to the node list, so `-{}-` still becomes an en
  dash. The encoder writes a second adjacent hyphen as `\mbox{-}`; the smoke's
  `ascii-dates` check caught the earlier form.
- A `TEXMFVAR` containing `/../` makes luaotfload fail with "no writeable cache
  path"; the root is always a clean absolute path.
- The moderncv contact icons put their glyph names ("Envelope", "MOBILE-ALT")
  into the text layer before the address. The contact check of task 3.7a
  ignores them and requires the address itself as a whole token
  ([document-verification.md](document-verification.md)).
- XeTeX applies Raleway's pair kerning as a gap in the text run, and pypdf
  reads a wide gap as a space: 2026-11 letters extracted as "Dear Hiring
  T eam," and "pgvector ,". Poppler joined them, so the split depended on the
  reader. The class now loads the body font with `Kerning=Off` (and
  `Numbers=Lining`, since Raleway's default figures are old-style), and the
  `word-coverage` check fails a word the layer holds only split
  ([document-verification.md](document-verification.md)). Lato Regular was
  tried as the body font and rejected: pypdf then extracted the page with no
  spaces between words at all. Of 30 stored letters, 24 held a split word;
  recompiled with the new options none did.
- The résumé had the same fault in Latin Modern Sans: pypdf read "AWS" as
  "A WS" and "WAVE" as "WA VE". The template loads the class's two families
  again through fontspec with `Kerning=Off`; the embedded fonts and the page
  count of the measured résumé stayed the same.
- Link annotations come out as the canonical URL (`?ref=a&b=2#top`,
  `%7Ealex_example/50%25`), checked with pypdf on the compiled PDFs.
- Compiling and verifying passed with all network access denied
  (`sandbox-exec` with `(deny network*)`).
- The upstream `cover.cls` names Lato 1 files (`Lato-Lig`, `Lato-Reg`, ...).
  The shipped Lato is the 2.015 family, so the class names were updated.
- Installing Poppler with Homebrew also upgraded eighteen shared formulae
  (libpng, fontconfig, pcre2, glib, xorgproto, cairo, ca-certificates, p11-kit,
  gnutls, libusb, readline, graphite2, harfbuzz, jpeg-turbo, xz, libtiff,
  little-cms2, openjpeg); the superseded versions stay until `brew cleanup`.

## What happens when a tool is missing

Measured with the built binary and `APPLYSMITH_DOCUMENT_TOOL_PATH` pointing at a
directory of symlinks that omits the tool (the venv scenarios use a fresh
`HOME`). Columns: `check` exit code, state and missing components, `smoke` exit
code, doctor status and exit code.

| Scenario | check | State and missing components | smoke | doctor `resumes` |
|---|---|---|---|---|
| no `lualatex` | 1 | unavailable: lualatex | 3 | warn, exit 0 |
| no `xelatex` | 1 | unavailable: xelatex | 3 | warn, exit 0 |
| no `kpsewhich` | 1 | unavailable: moderncv, tex-packages | 3 | warn, exit 0 |
| no TeX at all | 1 | unavailable: lualatex, xelatex, moderncv, tex-packages | 3 | warn, exit 0 |
| no `pdftotext` | 1 | unavailable: pdftotext | 3 | warn, exit 0 |
| no `pdftoppm` | 1 | unavailable: pdftoppm | 3 | warn, exit 0 |
| no Poppler at all | 1 | unavailable: pdftotext, pdftoppm | 3 | warn, exit 0 |
| no venv (fresh user) | 1 | unavailable: python-venv | 3 | warn, exit 0 |
| nothing installed | 1 | unavailable: all seven components | 3 | warn, exit 0 |

Unit tests (`probe_test.go`) cover the remaining cases with scripted tools:
`moderncv` missing or older than 2.4.1 while the engines are present, a missing
TeX package named in the detail, Poppler older than 26.05 or without a readable
version, Python older than 3.11, pypdf other than the locked version, a venv
that cannot import pypdf, an engine upgrade that makes a cached smoke stale,
and a failed smoke record. Nothing but new-PDF generation is affected: the
doctor leaf is a warning with the missing names and the fix, the exit code stays
0, and no probe creates a file.

## TeX package closure

Files the engines opened during the four smoke compiles (258 files from the
recorder `.fls` output, mapped to packages with the TeX Live database), plus
the Latin Modern and Font Awesome fonts that luaotfload loads without recording
them and the two engine formats. The lock holds one witness file per package;
`kpathsea`, `latexconfig`, `mptopdf` and `fontname` are infrastructure and have
no witness. Total read from the TeX tree: 9,451,954 bytes of package files and
11,222,538 bytes of formats (about 19.7 MiB).

| Package | TeX Live revision | Licence (tlpdb) | Files read | Bytes |
|---|---|---|---|---|
| academicons | 62622 | lppl1.3c ofl | 1 | 19357 |
| arydshln | 50084 | lppl1 | 1 | 46329 |
| atbegshi | 53051 | lppl1.3 | 1 | 24708 |
| atveryend | 72507 | lppl1.3c | 1 | 1695 |
| bigintcalc | 53172 | lppl1.3 | 1 | 40635 |
| bitset | 53837 | lppl1.3 | 1 | 33961 |
| cite | 36428 | other-free | 1 | 26218 |
| cm | 57963 | knuth | 16 | 20588 |
| colortbl | 71883 | lppl | 1 | 12726 |
| ctablestack | 38514 | lppl1.3 | 1 | 1510 |
| epstopdf-pkg | 71084 | lppl1.3 | 1 | 13886 |
| etoolbox | 73881 | lppl1.3c | 1 | 46850 |
| fancyhdr | 73783 | lppl1.3 | 1 | 31715 |
| fontawesome5 | 63207 | ofl lppl1.3c | 8 | 560177 |
| fontname | 74435 | gpl | 1 | 3524 |
| fontspec | 71237 | lppl1.3c | 3 | 169285 |
| geometry | 61719 | lppl1.3c | 1 | 41601 |
| gettitlestring | 53170 | lppl1.3 | 1 | 8371 |
| graphics | 72739 | lppl1.3c | 6 | 40961 |
| graphics-cfg | 41448 | pd | 2 | 2437 |
| graphics-def | 70970 | lppl1.3c | 2 | 34240 |
| hycolor | 53584 | lppl1.3 | 1 | 17914 |
| hyperref | 72773 | lppl1.3 | 6 | 460013 |
| iftex | 73115 | lppl1.3c | 4 | 10021 |
| import | 54683 | pd | 1 | 6201 |
| infwarerr | 53023 | lppl1.3 | 1 | 8356 |
| intcalc | 53168 | lppl1.3 | 1 | 31769 |
| kpathsea | 73918 | lgpl2.1 | 1 | 42148 |
| kvdefinekeys | 53193 | lppl1.3c | 1 | 5412 |
| kvoptions | 63622 | lppl1.3 | 1 | 22555 |
| kvsetkeys | 64632 | lppl1.3 | 1 | 13815 |
| l3backend | 71991 | lppl1.3c | 3 | 65638 |
| l3kernel | 73651 | lppl1.3c | 2 | 24770 |
| l3packages | 72128 | lppl1.3c | 2 | 14457 |
| latex | 73643 | lppl1.3c | 9 | 82840 |
| latexconfig | 68923 | n/a | 1 | 678 |
| lm | 67718 | gfl | 7 | 717152 |
| ltxcmds | 69032 | lppl1.3c | 1 | 17865 |
| lua-uni-algos | 62204 | lppl1.3 | 3 | 25305 |
| lualibs | 67994 | gpl2 | 6 | 283888 |
| luaotfload | 74324 | gpl2 | 34 | 1470193 |
| luatexbase | 52663 | lppl1.3 | 1 | 10170 |
| metalogo | 18611 | lppl | 1 | 7002 |
| microtype | 73928 | lppl1.3c | 7 | 210113 |
| moderncv | 71837 | lppl1.3c | 14 | 184698 |
| mptopdf | 74241 | n/a | 1 | 71627 |
| multirow | 72833 | lppl1.3 | 1 | 6696 |
| pdfescape | 53082 | lppl1.3 | 1 | 19007 |
| pdftexcmds | 55777 | lppl1.3c | 2 | 29536 |
| pgf | 71527 | lppl1.3c gpl2 fdl | 62 | 1214779 |
| realscripts | 56594 | lppl1.3 | 1 | 4558 |
| refcount | 53164 | lppl1.3 | 1 | 9878 |
| rerunfilecheck | 63869 | lppl1.3c | 1 | 9714 |
| stringenc | 52982 | lppl1.3 | 1 | 21514 |
| tex-gyre | 68624 | gfl | 1 | 2230 |
| tex-ini-files | 73863 | pd | 1 | 1020 |
| textpos | 63967 | lppl1.3c | 1 | 12842 |
| tipa | 29349 | lppl | 2 | 27740 |
| titlesec | 73302 | mit | 1 | 48766 |
| tools | 72739 | lppl1.3c | 2 | 24766 |
| unicode-data | 73362 | lppl1.3c | 7 | 2745410 |
| uniquecounter | 53162 | lppl1.3 | 1 | 7008 |
| url | 32528 | lppl | 1 | 12796 |
| xcolor | 72484 | lppl1.3c | 1 | 55384 |
| xltxtra | 56594 | lppl1.3c | 1 | 5686 |
| xunicode | 30466 | lppl1.3 | 1 | 205722 |
| zapfding | 61719 | gpl | 1 | 1528 |

## Shipped source hashes

Original hashes are of the files at ai-job-search commit
`a011214d60f943770e33f7d5f070e1a5f91e3822` (fonts: of the CTAN and foundry
files, which are identical). The exact-file manifest with modifications and
licence evidence is [third_party/document-sources.json](../../third_party/document-sources.json).

| File | Original SHA-256 (upstream) | Shipped SHA-256 |
|---|---|---|
| `cover.cls` | `db606a00acbec168ce93fdd70b6cc380ccd0a4b8c62fea71b21e503f9e72d39e` | `ff42dab01997d03cb526bb8f3557153ee2f42dee264236285635719780629ceb` |
| `cover.tex` | `b6408a865d110a63ebaf032a7a8bd403b5d1183f6835e0e886c5f2a042960628` | `1823f277be3d4c6da17a81a78f42fe9882c6904864ec4fcfe8e7af8aa21963c5` |
| `fonts/lato/Lato-Bold.ttf` | `bf1b8130069b44b9148eeece35e5423bedac49777ba746615b826b8276574a7b` | `bf1b8130069b44b9148eeece35e5423bedac49777ba746615b826b8276574a7b` |
| `fonts/lato/Lato-Hairline.ttf` | `80dc280e8a05b80d7a566a15b379bba422a776e5bc58b92a9b8f3a9bbea7eb03` | `80dc280e8a05b80d7a566a15b379bba422a776e5bc58b92a9b8f3a9bbea7eb03` |
| `fonts/lato/Lato-Italic.ttf` | `8c863f833b12bc8486671d26e3a58b13b0f9eaff1d6b10398e7c107a9b7ee05a` | `8c863f833b12bc8486671d26e3a58b13b0f9eaff1d6b10398e7c107a9b7ee05a` |
| `fonts/lato/Lato-Light.ttf` | `e9d59afb6d9cb9cf6e8d8159d4639d5b577e29efc64a15182f228659cfc1e818` | `e9d59afb6d9cb9cf6e8d8159d4639d5b577e29efc64a15182f228659cfc1e818` |
| `fonts/lato/Lato-LightItalic.ttf` | `8926c41622b7400d7f6f7dbf31cea50495f31230cf40904b8b83e634bf3cf6c6` | `8926c41622b7400d7f6f7dbf31cea50495f31230cf40904b8b83e634bf3cf6c6` |
| `fonts/lato/Lato-Regular.ttf` | `6f6940be0835c3ddec9199e5fc42be4cbc61ebcfd58c623fdf719366253f1780` | `6f6940be0835c3ddec9199e5fc42be4cbc61ebcfd58c623fdf719366253f1780` |
| `fonts/raleway/Raleway-ExtraLight.otf` | `e19adb1e85c11d4982d8a12ff351237da5f0d1c07ceb01b3687045619ba2d864` | `e19adb1e85c11d4982d8a12ff351237da5f0d1c07ceb01b3687045619ba2d864` |
| `fonts/raleway/Raleway-Medium.otf` | `84fdac3c3378ba1518315a71867cfdcba2ae2658bcbf40a51d6704640ffbd5af` | `84fdac3c3378ba1518315a71867cfdcba2ae2658bcbf40a51d6704640ffbd5af` |
| `requirements.txt` | none (product-owned) | `a3a8a8db971d48fe0a0bf74227e241b13166bd2df2e4daa8513c83eefff9b011` |
| `resume.tex` | `54645495edc618ef829ed99f752b02cd296f156a590b750440722d8fb031fd80` | `0da2b0961b58b875e85c6423b808d1df628abb9c76baf4c499b690cfa2f88f3b` |
| `verify_layout.py` | `c876e62bdf36ec3d1d662bb92871265df1eafb4aa8942fda6cf5b11622af4bd3` | `99140f1f912d25a3f6cfa13e4f3544e3f9607f3700064bbd2ca75af972582471` |
| `verify_pdf.py` | `1763f8f37f672a16faba76380fb3f9703a0d4e99512117dd166ebc68af030db9` | `ab202d0de8e7e50a973f0791195245aec75c08f331afc56f4f65173544bae466` |

## Footprint

Measured separately because the app target is 80 MB and excludes every
external tool.

**App.** `go build ./cmd/applysmith`: 26,218,162 bytes before, 30,621,730 after
(+4,403,568 bytes, 4.2 MiB). The embedded files are 4,188,549 bytes, of which
the eight fonts are 3.9 MB; Lato 2.015 is about 650 KB per weight where the
upstream's Lato 1 subsets were about 120 KB. Tracked source adds about 4.2 MB
to the repository (about 2 MB less now that the fonts are stored gzip-compressed).

**External tools, added by this proof** (the machine already had TeX Live 2025
and Python 3.14):

| Item | On disk |
|---|---|
| Poppler 26.10.0 and its 12 new formulae (`brew install poppler`) | 82,760 KiB (80.8 MiB) in the Cellar; poppler itself 34.5 MiB |
| Poppler's upgraded shared formulae | old versions kept until `brew cleanup`; `/opt/homebrew` grew 210,884 KiB (206 MiB) in total |
| Poppler bottle downloads | 96,304 KiB (94 MiB) in `~/Library/Caches/Homebrew`, removable |
| Private venv (Python 3.14 venv, pip 26.2.1, pypdf 6.19.0) | 17,452 KiB (17.0 MiB) |
| Private TeX cache (luaotfload font database and font caches) | 5,356 KiB (5.2 MiB) |
| Whole per-user toolchain directory after a smoke | 22,816 KiB (22.3 MiB) |
| Smoke builds | transient, removed after the run; each document is under 40 KiB |

**External tools, already present.** The full TeX Live 2025 tree is 9.1 GiB
(`/usr/local/texlive/2025`); the files the templates read are about 19.7 MiB
(table above). A minimal installation (BasicTeX plus the 64 witness packages)
was not installed or measured here, so its size is unknown. Homebrew
`python@3.14` is 87.6 MiB.

## Not covered by this task

- `GET /api/documents/toolchain` (the same read-only report over HTTP).
- `make verify-documents` is a target that runs the opt-in real-tool tests: the
  `TestToolchain_Real*` tests of the toolchain package, then the
  `TestDocuments_Real*` source-to-PDF flows of the documents package (the
  paper and page-limit matrix, the page budget, overflow and thin-final-page
  failures, and a draft followed by its PDF). It is not yet part of
  `make verify` or a release check, as the spec requires from task 3.10.
- Windows and Linux executable discovery; only macOS paths are probed.
- A human visual review of the previews; the four mechanical checks passed and
  the A4 résumé and letter previews were looked at once.
- Final-artifact verification (contact, word preservation, reading order,
  dates, percentages, fonts, geometry profiles, the stored verdict and the
  drawer) is [document-verification.md](document-verification.md).
- Adversarial coverage with the installed engine (cancellation killing the
  process group, deadline, output cap, literal payloads) is in
  `toolchain/real_test.go`, argv, limits and forged names in `argv_test.go`; the
  scratch limit is checked after a pass, not during it. Repair of a build that
  fails its budget or layout:
  [document-layout-repair.md](document-layout-repair.md).

## Local CV text import

`toolchain.ExtractCVText` is the shared CLI/dashboard extraction boundary for
PDF, DOCX, Markdown and plain text. It reads selected bytes only: 10 MiB input,
256 KiB UTF-8 output without NUL bytes, and at least 200 characters after
trimming surrounding whitespace. Short extraction asks the user to paste text
instead. PDF uses the existing bounded Poppler
adapter, independently of TeX/Python rendering readiness. A scanned PDF or a
missing Poppler tool is an actionable refusal, not an empty saved CV.

DOCX uses standard-library ZIP/XML parsing. Package relationships select the
main document and referenced headers/footers, including a renamed main part;
external relationships and embedded objects are not opened. Expanded XML has
an aggregate 10 MiB bound and emitted text has the same 256 KiB CV bound.
Malformed archives/XML, duplicate parts and missing internal targets fail.
A list paragraph (`w:numPr` with a numId other than 0) has no bullet in its
text, since Word draws it from the numbering, so the extract writes "• " before
it, once per paragraph.
WordprocessingML paragraph/run/text structure follows the
[Microsoft Open XML documentation](https://learn.microsoft.com/en-us/office/open-xml/word/how-to-open-and-add-text-to-a-word-processing-document).
Formatting is not reproduced; the result remains an editable review.

A text with no Markdown heading (a PDF's or DOCX's, a plain `.txt`) is marked
up by `cvparse.Markdown` the way `cv.md` is read, so the preview, the profile
draft's identity (`cvparse.Identity` reads the `#` name and the contact line
under it) and the résumé manifest's sections (`documents.parseCV`) see its
structure: a first line of two to five capitalised words becomes `# Name`; a
whole-line section label (the plain-text labels `cvparse` already reads)
becomes `##`; under a section, a line shaped "Title, Organisation (2021-03 to
present)" with a year range becomes `###`; a line led by a bullet glyph
(including Word's U+F0B7) becomes `- `. Words and order never change, and a
text that already has a heading is left as it is.

`POST /api/setup/cv/extract?name=<filename>` uses the authenticated, remote-denied
10 MiB raw-upload route and returns `CVExtracted`; it writes nothing. Both CV
editors use it before the existing explicit save, and the interactive CLI shows
text and requires confirmation before its existing writer runs. The separate
`applysmith cv import <file>` command prints text and the parsed draft, defaults to
no, and uses the workspace lock/revision-aware CV writer without opening a
database or changing profile facts. The shared CV parser recognizes exact
plain section labels as well as Markdown headings, retains source line/section
locators, and clears prior-entry context on section changes. Parsed history
still requires its separate selection/confirmation. File/dependency budgets and
public/remote allowlists are unchanged.
