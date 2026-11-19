# Document assets: behavioural spec

The five document assets in `internal/documents/assets` are written for this
product: the résumé template `resume.tex`, the letter template `cover.tex` and
its class `cover.cls`, and the two verifier scripts `verify_pdf.py` and
`verify_layout.py`. This page is the contract they implement. The Go side of
the contract is `assets/render.go` (template data and encoders) and
`toolchain/verify.go` (the parser that accepts or refuses a verdict); where
this page and the Go code disagree, the Go code is right and this page is
fixed. The checks and their meaning are in
[document-verification.md](document-verification.md); the tools, engines and
pins in [document-toolchain.md](document-toolchain.md).

## Shared rules

- Python scripts use the standard library and pypdf only. They are run as
  `<venv python> -I - <args>` with the script on standard input, so they import
  nothing from their own directory and read no environment.
- A script prints exactly one JSON object (ASCII-escaped, keys sorted, one
  trailing newline) and exits 0 once it has printed it; the verdict is in the
  document. Exit 2 is a usage error (an argument the parser refuses) and exit 3
  an internal error (the PDF cannot be read from disk, a requested file cannot
  be written); both print one line on standard error and nothing on standard
  output.
- The PDF is read from disk once. `pdf_sha256` and `pdf_bytes` describe exactly
  those bytes, and every measurement is made on them.
- Every check is `pass`, `fail` or `unverified`. A check that could not run is
  `unverified`, never `pass`. `overall` is `fail` when any check failed, else
  `unverified` when any check is unverified or there are no checks, else `pass`.
- A check's `reason` is empty on a pass. On a fail it lists the problems joined
  by `"; "`. `verify_pdf` reasons never quote document text; `verify_layout`
  reasons may quote short excerpts (at most 60 characters) because they only
  reach private diagnostics. An unverified reason names the cause (the
  exception class, the exit code), never an exception message.
- `python` is the interpreter version as `major.minor.micro`.
- `schema_version` is 1; `script_version` is `"3"` for both scripts.

## verify_pdf.py

Arguments: `<pdf> [--pages N] [--max-pages N] [--min-chars N] [--contains TEXT]... [--ascii-dates] [--dump-text PATH]`.
`--min-chars` defaults to 1; `--contains` repeats.

Result fields: `schema_version`, `tool` (`"verify_pdf"`), `script_version`,
`pdf_sha256`, `pdf_bytes`, `python`, `pypdf` (its version, or null when it
cannot be imported), `extractor` (always `"pypdf"`), `pages`, `text_sha256`,
`font_usage`, `checks`, `overall`.

**Text layer.** The text of every page as pypdf extracts it (an empty string
for a page with none), joined with one `\n` between pages and nothing added at
the end. `text_sha256` is the SHA-256 of that string in UTF-8. When pypdf cannot
be imported, or opening the PDF or extracting any page raises, `pages`,
`text_sha256` and `font_usage` are null and every requested check is
unverified.

**Folding** for comparisons: Unicode NFC; left and right single quotes become
`'`, left and right double quotes `"`, en and em dash `-`, no-break space a
space; then every run of whitespace becomes one space and the ends are trimmed.

**Checks**, in this order, each only when requested:

| Check | Requested | Fails when |
|---|---|---|
| `pdf-readable` | always | never fails; unverified when the PDF could not be opened |
| `text-layer` | always | the folded layer has fewer characters than `--min-chars`; the reason gives both numbers |
| `page-count` | `--pages` | the page count differs; the reason gives both numbers |
| `max-pages` | `--max-pages` | there are more pages than allowed |
| `required-text` | one or more `--contains` | a folded `--contains` value is not a substring of the folded layer; the reason lists the missing values by their 1-based position (`#1, #3`), never their text |
| `ascii-dates` | `--ascii-dates` | in the unfolded layer, a year (19xx or 20xx, four digits with no digit on either side) is followed by optional spaces or tabs (no line break) and a dash from U+2010 to U+2015 or U+2212, or such a dash is followed by optional spaces or tabs and a year. The reason gives the number of hits and each dash as `U+XXXX` |

**Font usage.** Every text run pypdf reports to its extraction visitor is
counted: the letters in it (characters for which `str.isalpha` is true), keyed
by the font's `/BaseFont` name without its leading slash and without a subset
prefix of six capital letters and `+` (`"unknown"` when the run has no font
or the font no `/BaseFont`), and by the run's font size as the visitor
receives it, rounded to one decimal (0 when absent). Runs without letters are
not counted. The list is sorted by letters descending, then font name, then
size, and holds at most 64 entries of `{font, size_pt, letters}`. It is null
when the walk raises.

**Text dump.** With `--dump-text`, when a text layer was extracted, it is
written to the path in UTF-8 with a trailing newline added if it has none;
missing parent directories are created. A write failure is exit 3.

## verify_layout.py

Arguments: `<pdf> --pdftotext <absolute path> --template resume|cover --paper a4|letter`, all required.

Result fields: `schema_version`, `tool` (`"verify_layout"`), `script_version`,
`profile` (`<template>-<paper>`), `pdf_sha256`, `pdf_bytes`, `python`,
`poppler`, `pages`, `measurements`, `checks`, `overall`.

**Extraction.** The bytes that were hashed are written to a private copy in a
new temporary directory created inside the PDF's own directory (the caller
removes that directory, so nothing is left in the system temp directory), and
`<pdftotext> -bbox -enc UTF-8 <copy> -` runs on it with a 25 second timeout.
The extractor has failed when the path is not absolute or not a file, the run
times out, exits non-zero or cannot start, or the output holds no page. Then
`poppler` is `""`, `pages` null, `measurements` empty and every check
unverified with a reason naming the failure (and the exit code, when there is
one). After a successful extraction `poppler` is the first line of the
standard error of `<pdftotext> -v` (10 second timeout), stripped, or `""`.

The output is XHTML: one `<page width=… height=…>` per page holding
`<word xMin=… yMin=… xMax=… yMax=…>text</word>` elements. Coordinates are
points, y growing downwards from the top of the page; word text is
HTML-unescaped.

**Lines.** On each page the words are grouped by their `yMin` rounded to a
whole point with Python's `round`. A group is one line: its top is the
smallest `yMin`, its bottom the largest `yMax`, its left edge the smallest
`xMin`, its height the largest word height (`yMax - yMin`), and its text the
words ordered by `(xMin, yMin, yMax, text)` and joined by single spaces. Its
text start is its left edge, except when the line opens with one or more words
made only of list-marker glyphs (U+2022, U+2023, U+25AA, U+25CF, U+00B7) and has
a word after them: then it is the `xMin` of the first word after the markers.
A page's lines are ordered by top (stable). The **body** of a page is the lines
whose top is above `page height - 90`. A page with no body line is empty.
The **median line height** of a page is the body heights sorted ascending,
element `n // 2` (0 for no body). A line is a **heading** when its height is
more than 1.25 times the median, and a **section heading** when it is also
less than 2.0 times the median (the name above the document is taller still).
A line **has text** when its text contains a word character (`\w`).

**Profiles.** One per template and paper:

| Profile | Page (pt) | Left text edge | Right text edge | Entry gap limit |
|---|---|---|---|---|
| `resume-a4` | 595.28 x 841.89 | 59.5 | 59.5 | 24.0 |
| `resume-letter` | 612 x 792 | 61.2 | 61.2 | 24.0 |
| `cover-a4` | 595.28 x 841.89 | 79.4 | 79.4 | none |
| `cover-letter` | 612 x 792 | 79.4 | 79.4 | none |

The left and right values are the distance from the paper edge to the
template's text, measured on the compiled templates; they are re-measured when
a template's geometry changes. The hole limit is 100 pt for every profile.

**Checks**, in this order; `entry-gap` only for the résumé profiles. Page
numbers in reasons are 1-based (`p2`).

| Check | Scope | Fails when |
|---|---|---|
| `page-text` | each page | the page is empty |
| `page-size` | each page | width or height differs from the profile's by more than 1.5 pt |
| `clipping` | every word of each page, body or not; at most 5 problems per page | a word box leaves the page by more than 1.5 pt on any side; else it starts more than 14 pt left of the profile's left edge or ends more than 14 pt right of `width - right edge` (the reason says which margin); else its top is within 18 pt of the top of the page or its bottom within 18 pt of the bottom |
| `text-overlap` | every word of each page; at most 5 problems per page | two words' cores overlap by more than 30 percent of the smaller core's area. A word's core is its box with the top and bottom 20 percent of its height removed (a word box is sized by the font's bounding box, which for a large name or an icon reaches into the next line); each pair is considered once |
| `internal-hole` | each non-empty page | the largest gap is over 100 pt |
| `early-page-end` | each non-empty page that is not the last | the bottom share is over 0.25 |
| `footer-collision` | each non-empty page | among all lines of the page (not only the body) whose top is at or below `height - 90`, there is more than one distinct top rounded to 0.1 pt: one line there is a page number, two mean body text spilled into the band |
| `final-page-thin` | the last page, only when there is more than one page and it is not empty | the bottom share is over 0.35 |
| `orphaned-entry` | each pair of consecutive non-empty pages | see below |
| `entry-gap` | the document | see below |

**Measurements**, one per page, numbers rounded with Python's `round`
(points to 0.1, the share to 0.001). An empty page has only `page`,
`height_pt` and `empty: true`. A non-empty page also has `text_top_pt` (top
of the first body line), `text_bottom_pt` (bottom of the last body line in
top order), `bottom_space_pt` (height minus the lowest bottom of any body
line), `bottom_share` (bottom space over height), `largest_gap_pt` and
`largest_gap_y_pt`. The largest gap is taken over the distinct body-line tops
rounded to 0.1 pt and sorted: the largest difference between neighbours, and
the upper of the two tops; between equal differences the lower one on the page
wins. With fewer than two distinct tops both are 0.

**Orphaned entry.** The document's left margin is the smallest left edge of any
body line on any page; a line is indented when its left edge is more than 8 pt
right of it. A set of lines starts its text at the smallest text start among
those of its lines that have text, or nowhere when none has text. For each pair
of consecutive non-empty pages, with `last` the last body line of the first
page and `first` the first body line of the next:

1. If `last` is a heading (by its own page's median), the heading is stranded:
   the reason names it and contains the words "section heading". Go to the next
   pair.
2. The tail is the body lines of the first page whose top is at most 3 pt above
   `last`'s. If the tail starts its text nowhere (a list marker extracted as a
   line of its own) and `last` is not indented while `first` is, a list item
   is split across the break. Either way, go to the next pair.
3. The opener is the first body line of the next page that has text and whose
   text start is at most 60 pt right of the document's left margin (a line set
   further in is a right-aligned or centred line, not where the body resumes).
   Without one, go to the next pair.
4. The head is the next page's body lines whose top is within 3 pt of the
   opener's. When the head starts its text more than 8 pt right of where the
   tail starts its text, an entry header was left behind its content; the reason
   quotes the tail's longest line that has text and the opener.

**Entry gap** (résumé only). Pages in order; on each page a running reach starts
unset and lines are visited in body order. Until the document's first section
heading has been met, each line is tested for being one (by its page's median);
the heading itself is not tested for a gap. After it, a line whose top is more
than the limit below the reach is a problem (the reason gives the gap in whole
points and quotes the line). Every visited line, before or after the heading,
then sets the reach to the larger of the reach and its bottom. The reach is
measured from the lowest bottom so far because a bullet or an icon shares its
line with text and can stand lower or higher than it.

## Templates

Both templates are Go `text/template` sources with the delimiters `<%` and
`%>` and `missingkey=error`. Every action that prints is a single call of one
of the encoders `tex` (plain text), `rich` (text with `**bold**` markers),
`url` (a validated link target) or `paper` (the class option of the paper);
`TestTemplatesCallOnlyEncoders` enforces it. The data are `assets.Resume` and
`assets.Cover`; an empty optional field prints nothing, not an empty command.
The templates never read files beyond the TeX packages and, for the letter,
the class and fonts copied beside the source, and compile without shell escape
or network.

### Résumé (`resume.tex`, LuaLaTeX)

- moderncv 2.4.1 or later, banking style, `sans` option, `11pt` as the first
  class option, the paper from `paper .Paper`, and the page geometry set by
  `\usepackage[scale=0.80]{geometry}`. Text is set in the class's Latin Modern
  Sans; no other font may set letters (Font Awesome is allowed only because
  moderncv loads it). All body text is the class size: hierarchy comes from
  weight, the section headings and the name only.
- One accent colour, a deep blue (RGB 28, 61, 105), for the name (both parts),
  the section headings and their rules and the bullet markers; the contact line
  is the class's dark grey and all other text black.
- The PDF title is `.Title`; links are live (`\href` with the `url` encoder).
- Title block: first and last name, extracting as two words separated by a
  space; then the address, phone, email (a `mailto:` link) and each of `.Links`
  (its label linking to its URL), each only when set. Contact values are plain
  text: no icon glyphs, whose names would land in the text layer.
- Sections, each only when it has content and headed by its label from
  `.Labels`: summary (`.Profile`, rich), competencies (each a line with its
  label and rich text), experience, projects, education, languages (text).
  An entry shows its dates, title, organization, location and note when set,
  and its bullets (rich) as a list. A project shows its title and bullets.
  The entry header has title and organization on the left, wrapping within
  the width the right column leaves, and location and dates on the right,
  level with its first line, so the two never run into each other.
- Spacing: 7 pt after each entry and a list with 2 pt above it and 2 pt
  between items, so sections are further apart than entries and entries
  further than bullets. With `.Tight` it is 5 pt after each entry and a list
  with no space above it, between items or between paragraphs. Nothing else differs
  between the two modes, and no explicit vertical space is under 1 pt.
- A section heading is never the last line of a page with its first entry on
  the next, and an entry's header lines never end a page without its first
  bullet (reserve the space before each heading and entry).
- A long word that cannot break (a path, a command name) loosens its line
  rather than running into the right margin (`\emergencystretch`).
- An ASCII hyphen in the source (date ranges) is `U+002D` in the text layer.
- No running header or footer.

### Letter (`cover.tex` with `cover.cls`, XeLaTeX)

- `cover.tex` loads the class `cover` from `cover.cls` beside it, with the paper
  from `paper .Paper`. The build copies the fonts beside the source as
  `fonts/lato/Lato-{Bold,Hairline,Italic,Light,LightItalic,Regular}.ttf` and
  `fonts/raleway/Raleway-{ExtraLight,Medium}.otf`; fonts are loaded from those
  paths only, never from the system.
- The name at the top is set in Lato above 14 pt. Every letter at 14 pt or below
  is set in one font, Raleway Medium at 11 pt on 14 pt, loaded with kerning off
  (a text extractor reads a kerned pair as a word break) and lining figures.
  That font must also print an ASCII hyphen as `U+002D`.
- Order on the page: the name; the contact line (`.Contact`, items separated by
  `|`, the separator disappearing where the line wraps, so no line starts or
  ends with one; a link item is a live link); the profile links (`.Links`) on a
  line of their own below it; a thin rule across the text width; the date; each `.Recipient` line on its own line; the
  salutation; the blocks; the closing; two empty lines for a signature; the
  name.
- A block is a paragraph (rich) followed, when it has bullets, by a list in the
  same body font. A block with an empty text prints its list only, never an
  empty paragraph (XeLaTeX stops on it). The space above and below a list equals
  the space between paragraphs; list items are 4 pt apart.
- The name, the rule and the list markers are in the résumé's accent colour,
  the contact and link lines dark grey, everything else black.
- The closing carries no forced line break of its own.
- Left and right margins are equal, 2.8 cm; one page; no page number or
  other running header or footer.
- The PDF title is `.Title`.

## Comments in the assets

Header comments say what the file is and why non-obvious choices were made, in
English, without dates or names of people or other projects.
