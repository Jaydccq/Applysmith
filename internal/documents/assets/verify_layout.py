#!/usr/bin/env python3
# Adapted from tools/verify_layout.py of ai-job-search (MIT, Copyright (c) 2026
# Mads Lorentzen), https://github.com/MadsLorentzen/ai-job-search at commit
# a011214d60f943770e33f7d5f070e1a5f91e3822. Licence text:
# third_party/licenses/MIT-ai-job-search.txt; manifest:
# third_party/document-sources.json.
#
# Changes: the result is one schema-versioned JSON document on stdout instead
# of console text; each layout property is its own pass, fail or unverified
# check; Poppler is addressed by an explicit absolute path (no PATH lookup) and
# a missing or broken extractor makes every check unverified, never a pass; the
# PDF is read once and the verdict carries the SHA-256 of exactly those bytes,
# which Poppler also reads. The geometry classes and the original thresholds
# are unchanged. Added for this product: thresholds are a profile per template
# and paper size, selected by --template and --paper and echoed in the result;
# page-size, clipping, text-overlap and (résumé only) entry-gap checks; the
# footer band and the early-page-end and thin-final-page limits come from the
# profile.
"""Measure a compiled document's page layout from Poppler word boxes; prints JSON.

Contract (schema_version 1): the process prints one JSON object and exits 0
once a complete result was written; the verdict is in the document. Exit 2 is
a usage error and exit 3 an internal error, both without a result. Page count
is verify_pdf.py's job and is not checked here.

Properties checked, each as one check: empty page, page size, clipping,
overlapping text, internal hole, early page end, footer collision, thin final
page, orphaned entry or heading and, for the résumé profile, a large gap
between entries. A reason may quote a short excerpt of the document text, so it
belongs in private diagnostics only.

The thresholds are the PROFILES below, one per template and paper size,
measured on the moderncv and cover class geometry of internal/documents/assets.
A one-page document is never "early" or "thin": both checks apply only to a
document that has more than one page.
"""

from __future__ import annotations

import argparse
import hashlib
import html
import json
import re
import subprocess
import sys
import tempfile
from dataclasses import dataclass
from pathlib import Path

SCHEMA_VERSION = 1
SCRIPT_VERSION = "2"
TOOL = "verify_layout"

PASS, FAIL, UNVERIFIED = "pass", "fail", "unverified"

# A gap larger than this between consecutive lines is a hole, not spacing. Section
# spacing in the stock templates runs to roughly 45pt; 100pt is about seven lines.
GAP_LIMIT_PT = 100.0

# Bottom whitespace on a page that is not the last one. The stock geometry leaves ~85pt.
BOTTOM_LIMIT_FRACTION = 0.25

# A final page emptier than this reads as an unfinished document.
LAST_PAGE_THIN_FRACTION = 0.35

# The page-number footer lives in the bottom margin and is a text line like any other to
# Poppler. Ignore this band when measuring the body, or every page looks like it has a
# hole above its footer.
FOOTER_BAND_PT = 90.0

# A line indented at least this far past the page's left edge is a bullet or a
# continuation, not an entry header or a section heading.
INDENT_PT = 8.0

# Glyphs that label a list item when a template merges them into the item's first line.
# Unicode only: ASCII `-` and `*`, and the dashes, also open real text (the continuation
# of a bullet may start with an em dash), so a line opening with one is read as text.
LIST_MARKERS = frozenset("\u2022\u2023\u25aa\u25cf\u00b7")

# Lines whose tops are this close are parts of one printed line. pdftotext -bbox output
# is grouped by rounded yMin, which splits a printed line wherever words in another
# font sit a fraction of a point off and a .5 boundary falls between them (a bold word
# 0.2pt off; a list marker set 1.3pt below its item's first line on the stock CV).
# Consecutive printed lines are ~12pt apart.
SAME_PRINTED_LINE_PT = 3.0

# A list sets its text in by a label's width: 11-25pt past the margin on the stock CV
# and cover letter. A line whose text starts further in than this is set right or
# centred (a running header, a date on a line of its own), not where a page's body
# resumes.
SET_IN_LIMIT_PT = 60.0

# A line this much taller than the body median is a section heading. The name in
# the title block is taller still, so a section heading is below this ceiling.
HEADING_HEIGHT_RATIO = 1.25
SECTION_HEADING_CEILING = 2.0

# Slack, in points, for rounding in the word boxes and for a glyph's side bearing.
BOX_TOLERANCE_PT = 1.5

# Text may stand this far outside the template's text box before it counts as
# running into the margin (about one em of body text).
MARGIN_SLACK_PT = 14.0

# Text closer than this to the top or bottom edge of the page is clipped by most
# printers and by every ATS page renderer.
EDGE_BAND_PT = 18.0

# Two word boxes that share more than this fraction of the smaller box are one
# word printed over another.
OVERLAP_FRACTION = 0.3
CORE_MARGIN = 0.2


@dataclass(frozen=True)
class Profile:
    """Thresholds for one template on one paper size.

    left and right are the template's margins, from its geometry. entry_gap is the
    largest gap, bottom of one line to top of the next, tolerated between lines
    that follow the first section heading; None for a template without entries.
    """

    id: str
    width: float
    height: float
    left: float
    right: float
    gap_limit: float
    entry_gap: float | None


A4 = (595.28, 841.89)
LETTER = (612.0, 792.0)


def _profile(template: str, paper: str, size: tuple[float, float], left: float, right: float, entry_gap: float | None) -> Profile:
    return Profile(f"{template}-{paper}", size[0], size[1], left, right, GAP_LIMIT_PT, entry_gap)


# Left and right text edges are measured on the compiled synthetic documents. moderncv
# sets its right-aligned date and place column about 11pt past the margin its geometry
# names, so the résumé's right edge is closer to the paper than its left. entry_gap is
# about twice the largest gap the stock templates leave between lines after the first
# section heading (10pt).
PROFILES = {
    ("resume", "a4"): _profile("resume", "a4", A4, 59.0, 48.6, 24.0),
    ("resume", "letter"): _profile("resume", "letter", LETTER, 60.7, 50.3, 24.0),
    ("cover", "a4"): _profile("cover", "a4", A4, 65.2, 65.2, None),
    ("cover", "letter"): _profile("cover", "letter", LETTER, 65.2, 65.2, None),
}

PAGE_RE = re.compile(r'<page width="([\d.]+)" height="([\d.]+)">(.*?)</page>', re.S)
WORD_RE = re.compile(
    r'<word xMin="(-?[\d.]+)" yMin="(-?[\d.]+)" xMax="(-?[\d.]+)" yMax="(-?[\d.]+)">([^<]*)</word>'
)
TEXT_RE = re.compile(r"\w")


@dataclass
class Line:
    top: float
    bottom: float
    left: float
    height: float
    text: str
    # Where the text starts once leading LIST_MARKERS words are set aside; None when the
    # line has no such marker, so a Line built from `left` alone behaves as before.
    text_left: float | None = None

    @property
    def text_x(self) -> float:
        return self.left if self.text_left is None else self.text_left


@dataclass
class Word:
    x0: float
    y0: float
    x1: float
    y1: float
    text: str


class Page:
    def __init__(self, width: float, height: float, lines: list[Line], words: list[Word] | None = None):
        self.width = width
        self.height = height
        self.lines = sorted(lines, key=lambda l: l.top)
        self.words = words or []

    @property
    def body(self) -> list[Line]:
        cutoff = self.height - FOOTER_BAND_PT
        return [l for l in self.lines if l.top < cutoff]

    @property
    def empty(self) -> bool:
        return not self.body

    @property
    def bottom_space(self) -> float:
        return self.height - max(l.bottom for l in self.body) if self.body else self.height

    @property
    def left_edge(self) -> float:
        return min(l.left for l in self.body) if self.body else 0.0

    @property
    def body_median_height(self) -> float:
        heights = sorted(l.height for l in self.body)
        return heights[len(heights) // 2] if heights else 0.0

    @property
    def footer_crowded(self) -> bool:
        """One line in the bottom band is a page number; two means body text spilled in."""
        band = self.height - FOOTER_BAND_PT
        return len({round(l.top, 1) for l in self.lines if l.top >= band}) > 1

    def is_indented(self, line: Line) -> bool:
        return line.left > self.left_edge + INDENT_PT

    def is_heading(self, line: Line) -> bool:
        median = self.body_median_height
        return bool(median) and line.height > median * HEADING_HEIGHT_RATIO

    def largest_gap(self) -> tuple[float, float]:
        """Largest top-to-top distance between body lines, and where it starts.

        Measured top-to-top rather than bottom-to-top, so a tall line inflates the gap
        by its own height (a 160pt void under a heading reads as ~174pt). That errs
        toward over-detection, which is the right direction for a check whose job is
        to stop a hole from shipping.
        """
        tops = sorted({round(l.top, 1) for l in self.body})
        if len(tops) < 2:
            return (0.0, 0.0)
        return max((tops[i + 1] - tops[i], tops[i]) for i in range(len(tops) - 1))


class ExtractorError(Exception):
    """Poppler is missing or could not produce word boxes for this PDF."""


def parse_pdf(path: Path, pdftotext: Path) -> tuple[list[Page], str]:
    if not pdftotext.is_absolute() or not pdftotext.is_file():
        raise ExtractorError("pdftotext was not found at the given absolute path")
    try:
        out = subprocess.run(
            [str(pdftotext), "-bbox", "-enc", "UTF-8", str(path), "-"],
            capture_output=True,
            text=True,
            encoding="utf-8",
            errors="replace",
            check=True,
            timeout=25,
        ).stdout
    except subprocess.TimeoutExpired as exc:
        raise ExtractorError("pdftotext timed out") from exc
    except (subprocess.CalledProcessError, OSError) as exc:
        # An xpdf build has no -bbox flag, and Poppler before 26.05 aborts on a
        # PDF whose Info dictionary holds an empty string. Both are a broken
        # extractor, not a broken document.
        code = getattr(exc, "returncode", "n/a")
        raise ExtractorError(f"pdftotext could not produce bounding boxes (exit {code})") from exc
    version = poppler_version(pdftotext)
    pages = []
    for pw, ph, body in PAGE_RE.findall(out):
        words = [
            Word(float(x0), float(y0), float(x1), float(y1), html.unescape(text))
            for x0, y0, x1, y1, text in WORD_RE.findall(body)
        ]
        buckets: dict[float, list[tuple[float, float, float, str]]] = {}
        for word in words:
            key = round(word.y0, 0)  # words on one line share a rounded yMin
            buckets.setdefault(key, []).append((word.x0, word.y0, word.y1, word.text))
        lines = [
            Line(
                top=min(w[1] for w in ws),
                bottom=max(w[2] for w in ws),
                left=min(w[0] for w in ws),
                height=max(w[2] - w[1] for w in ws),
                text=" ".join(w[3] for w in sorted(ws)),
                text_left=text_start(sorted(ws)),
            )
            for ws in buckets.values()
        ]
        pages.append(Page(float(pw), float(ph), lines, words))
    if not pages:
        raise ExtractorError("pdftotext produced no pages")
    return pages, version


def poppler_version(pdftotext: Path) -> str:
    try:
        err = subprocess.run([str(pdftotext), "-v"], capture_output=True, text=True, timeout=10).stderr
    except (subprocess.SubprocessError, OSError):
        return ""
    return err.splitlines()[0].strip() if err.strip() else ""


def text_start(words: list[tuple[float, float, float, str]]) -> float | None:
    """xMin of the first word after leading LIST_MARKERS words, if there were any."""
    lead = 0
    while lead < len(words) and words[lead][3] and set(words[lead][3]) <= LIST_MARKERS:
        lead += 1
    return words[lead][0] if 0 < lead < len(words) else None


def printed_text_x(lines: list[Line]) -> float | None:
    """Where the text of one printed line starts, from the bbox lines that make it up."""
    xs = [l.text_x for l in lines if TEXT_RE.search(l.text)]
    return min(xs) if xs else None


def find_orphans(pages: list[Page]) -> list[str]:
    """A page ending on an entry header or section heading whose content resumes overleaf.

    Two shapes, both documented failures:
      * the last body line of a page is a section heading (stranded heading)
      * the next page's text starts further in than the text of the page's last printed
        line (an entry header), i.e. the entry was split across the break

    The second compares where the TEXT starts, not where the line starts. A template may
    merge a list marker into the line it labels, which moves the line's left edge but not
    its text: in the #481 repro a bullet's first line starts at x58.9 (the marker), level
    with the entry headers at x59.5, while its text and its continuation start at x72.1.
    Read by left edge, a bullet wrapping across the break looked like a header followed by
    indented text, and a real header followed by such a bullet looked level. On the stock
    CV the outer marker may extract as a line of its own; then the header's text (x70.4)
    counted as indented and a header orphaned from its bullets (x81.3) went unreported.

    Only LIST_MARKERS glyphs are set aside. Where a marker is not among them - the stock
    CV's outer marker extracts as a circle glyph on some font setups and as an emoji on others - the
    line keeps the marker's x, which is exactly what the left-edge rule read. That is how
    a stock `[Degree]` line and its description (both text at x70.4) stay reported when
    that marker merges into the header line (header x59.0), as they were. When the marker
    extracts as its own line, the two are level and nothing tells them from a wrapped
    line without font information, so neither this rule nor the left-edge one reports it.
    """
    problems = []
    # Indentation must be judged against the document's left margin, not each page's own
    # minimum: a page that *opens* with indented bullets would otherwise treat their
    # indent as its margin and report nothing.
    body_lines = [l for p in pages for l in p.body]
    if not body_lines:
        return problems
    doc_left = min(l.left for l in body_lines)

    def indented(line: Line) -> bool:
        return line.left > doc_left + INDENT_PT

    for i in range(len(pages) - 1):
        here, nxt = pages[i], pages[i + 1]
        if here.empty or nxt.empty:
            continue
        last, first = here.body[-1], nxt.body[0]

        if here.is_heading(last):
            problems.append(
                f"p{i + 1} ends on the section heading {last.text.strip()!r} with its content "
                f"on p{i + 2}. Shorten the entry that follows it, or let the heading and its "
                "first entry move to the next page together"
            )
            continue
        # The last printed line: the bbox lines within SAME_PRINTED_LINE_PT of the last one.
        tail = [l for l in here.body if l.top >= last.top - SAME_PRINTED_LINE_PT]
        last_x = printed_text_x(tail)
        if last_x is None:
            # moderncv puts an itemize marker in its own bbox line at the list's left
            # edge, so a list item split across the break looks like an un-indented
            # header followed by indented text. Different defect, different fix.
            if not indented(last) and indented(first):
                problems.append(
                    f"p{i + 1} ends on a lone list marker whose text continues on p{i + 2} "
                    f"({first.text.strip()[:60]!r}): a bullet is split across the page break. "
                    "Shorten the preceding content so the whole item fits on one page"
                )
            continue
        # The next page's first printed line with text, skipping a marker in its own line
        # and a line set right or centred.
        opener = next(
            (
                l
                for l in nxt.body
                if TEXT_RE.search(l.text) and l.text_x <= doc_left + SET_IN_LIMIT_PT
            ),
            None,
        )
        if opener is None:
            continue
        head = [l for l in nxt.body if abs(l.top - opener.top) <= SAME_PRINTED_LINE_PT]
        next_x = printed_text_x(head)
        if next_x is not None and next_x > last_x + INDENT_PT:
            shown = max((l for l in tail if TEXT_RE.search(l.text)), key=lambda l: len(l.text))
            problems.append(
                f"p{i + 1} ends on {shown.text.strip()[:60]!r} while the text on p{i + 2} "
                f"starts further in ({opener.text.strip()[:60]!r}): an entry header is "
                "orphaned from its bullets. Add \\needspace before that \\cventry, "
                "or shorten it"
            )
    return problems


def check(check_id, status, reason=""):
    return {"id": check_id, "status": status, "reason": reason}


CHECK_IDS = (
    "page-text",
    "page-size",
    "clipping",
    "text-overlap",
    "internal-hole",
    "early-page-end",
    "footer-collision",
    "final-page-thin",
    "orphaned-entry",
)
ENTRY_GAP_ID = "entry-gap"


def check_ids(profile: Profile) -> list[str]:
    return list(CHECK_IDS) + ([ENTRY_GAP_ID] if profile.entry_gap is not None else [])


def clipped(page: Page, profile: Profile) -> list[str]:
    """Words outside the page box, in the template's margins or in the edge bands."""
    out = []
    for word in page.words:
        t = BOX_TOLERANCE_PT
        if word.x0 < -t or word.y0 < -t or word.x1 > page.width + t or word.y1 > page.height + t:
            out.append(f"{word.text[:30]!r} extends outside the page")
        elif word.x0 < profile.left - MARGIN_SLACK_PT or word.x1 > page.width - profile.right + MARGIN_SLACK_PT:
            out.append(f"{word.text[:30]!r} runs into the {'left' if word.x0 < profile.left - MARGIN_SLACK_PT else 'right'} margin")
        elif word.y0 < EDGE_BAND_PT or word.y1 > page.height - EDGE_BAND_PT:
            out.append(f"{word.text[:30]!r} stands in the top or bottom edge band")
    return out


def overlaps(page: Page) -> list[str]:
    """Pairs of word boxes that cover each other: a collision, not a typeset line."""
    # Poppler sizes a word box by its font's bounding box, which for a large name or an
    # icon reaches well into the neighbouring lines. Only the middle of the box, where
    # the glyphs are, is compared.
    core = [(w, w.y0 + CORE_MARGIN * (w.y1 - w.y0), w.y1 - CORE_MARGIN * (w.y1 - w.y0)) for w in page.words]
    core.sort(key=lambda c: c[1])
    out = []
    for i, (a, a0, a1) in enumerate(core):
        for b, b0, b1 in core[i + 1 :]:
            if b0 >= a1:
                break
            ix = min(a.x1, b.x1) - max(a.x0, b.x0)
            iy = min(a1, b1) - max(a0, b0)
            if ix <= 0 or iy <= 0:
                continue
            smaller = min((a.x1 - a.x0) * (a1 - a0), (b.x1 - b.x0) * (b1 - b0))
            if smaller > 0 and ix * iy / smaller > OVERLAP_FRACTION:
                out.append(f"{a.text[:20]!r} overlaps {b.text[:20]!r}")
    return out


def section_heading(page: Page, line: Line) -> bool:
    median = page.body_median_height
    return bool(median) and median * HEADING_HEIGHT_RATIO < line.height < median * SECTION_HEADING_CEILING


def entry_gaps(pages: list[Page], limit: float) -> list[str]:
    """Gaps between the lines that follow the document's first section heading.

    Measured from the lowest bottom edge so far, because a bullet marker or an
    icon shares its line with text and can stand lower or higher than it.
    """
    out = []
    started = False
    for i, page in enumerate(pages, 1):
        reach = None
        for line in page.body:
            if not started:
                started = section_heading(page, line)
            elif reach is not None and line.top - reach > limit:
                out.append(f"p{i} has a {line.top - reach:.0f}pt gap before {line.text.strip()[:40]!r}")
            reach = line.bottom if reach is None else max(reach, line.bottom)
    return out


def measure(pages: list[Page], profile: Profile) -> tuple[list[dict], list[dict]]:
    """Return (per-page measurements, checks)."""
    ids = check_ids(profile)
    problems: dict[str, list[str]] = {i: [] for i in ids}
    measured = []
    for i, page in enumerate(pages, 1):
        if abs(page.width - profile.width) > BOX_TOLERANCE_PT or abs(page.height - profile.height) > BOX_TOLERANCE_PT:
            problems["page-size"].append(
                f"p{i} is {page.width:.0f}x{page.height:.0f}pt, the {profile.id} profile expects {profile.width:.0f}x{profile.height:.0f}pt"
            )
        problems["clipping"].extend(f"p{i} {m}" for m in clipped(page, profile)[:5])
        problems["text-overlap"].extend(f"p{i} {m}" for m in overlaps(page)[:5])
        if page.empty:
            problems["page-text"].append(f"p{i} contains no text")
            measured.append({"page": i, "height_pt": round(page.height, 1), "empty": True})
            continue

        gap, gap_y = page.largest_gap()
        share = page.bottom_space / page.height
        measured.append(
            {
                "page": i,
                "height_pt": round(page.height, 1),
                "empty": False,
                "text_top_pt": round(page.body[0].top, 1),
                "text_bottom_pt": round(page.body[-1].bottom, 1),
                "bottom_space_pt": round(page.bottom_space, 1),
                "bottom_share": round(share, 3),
                "largest_gap_pt": round(gap, 1),
                "largest_gap_y_pt": round(gap_y, 1),
            }
        )
        if gap > profile.gap_limit:
            problems["internal-hole"].append(
                f"p{i} has a {gap:.0f}pt hole at y{gap_y:.0f} (~{gap / 14:.0f} blank lines)"
            )
        if i < len(pages) and share > BOTTOM_LIMIT_FRACTION:
            problems["early-page-end"].append(
                f"p{i} ends {page.bottom_space:.0f}pt ({share * 100:.0f}%) early although more pages follow"
            )
        if page.footer_crowded:
            problems["footer-collision"].append(f"p{i} has body text inside the bottom margin band")
        if i == len(pages) > 1 and share > LAST_PAGE_THIN_FRACTION:
            problems["final-page-thin"].append(f"p{i} is the last page and {share * 100:.0f}% empty")

    problems["orphaned-entry"].extend(find_orphans(pages))
    if profile.entry_gap is not None:
        problems[ENTRY_GAP_ID].extend(entry_gaps(pages, profile.entry_gap))
    checks = [check(i, FAIL if problems[i] else PASS, "; ".join(problems[i])) for i in ids]
    return measured, checks


def overall(checks):
    statuses = {c["status"] for c in checks}
    if FAIL in statuses:
        return FAIL
    if UNVERIFIED in statuses or not checks:
        return UNVERIFIED
    return PASS


def main(argv=None) -> int:
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if reconfigure:
            reconfigure(encoding="utf-8")
    ap = argparse.ArgumentParser(description="Measure a PDF's layout; prints JSON.")
    ap.add_argument("pdf", type=Path)
    ap.add_argument("--pdftotext", type=Path, required=True, help="absolute path of Poppler's pdftotext")
    ap.add_argument("--template", choices=("resume", "cover"), required=True, help="threshold profile: the template")
    ap.add_argument("--paper", choices=("a4", "letter"), required=True, help="threshold profile: the paper size")
    args = ap.parse_args(argv)
    profile = PROFILES[(args.template, args.paper)]

    try:
        data = args.pdf.read_bytes()
    except OSError:
        print("verify_layout: the PDF cannot be read", file=sys.stderr)
        return 3

    result = {
        "schema_version": SCHEMA_VERSION,
        "tool": TOOL,
        "script_version": SCRIPT_VERSION,
        "profile": profile.id,
        "pdf_sha256": hashlib.sha256(data).hexdigest(),
        "pdf_bytes": len(data),
        "python": ".".join(str(n) for n in sys.version_info[:3]),
        "poppler": "",
        "pages": None,
        "measurements": [],
    }
    # Poppler reads a private copy of the exact bytes that were hashed. The copy
    # sits beside the PDF, in the build directory the caller removes, so a
    # terminated run leaves nothing in the system temp directory.
    with tempfile.TemporaryDirectory(prefix="verify-layout-", dir=args.pdf.parent) as tmp:
        copy = Path(tmp) / "input.pdf"
        copy.write_bytes(data)
        try:
            pages, version = parse_pdf(copy, args.pdftotext)
        except ExtractorError as exc:
            result["checks"] = [check(i, UNVERIFIED, str(exc)) for i in check_ids(profile)]
        else:
            result["poppler"] = version
            result["pages"] = len(pages)
            result["measurements"], result["checks"] = measure(pages, profile)
    result["overall"] = overall(result["checks"])
    json.dump(result, sys.stdout, ensure_ascii=True, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
