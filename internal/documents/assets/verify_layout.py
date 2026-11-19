"""Layout verifier for a compiled document PDF.

Run as  <python> -I - <pdf> --pdftotext <absolute path>
                         --template resume|cover --paper a4|letter
with this script on standard input. It reads the PDF once, has Poppler's
pdftotext report every word with its box, rebuilds the lines of each page
and checks the geometry against the profile of the template and paper. It
prints one JSON verdict on standard output (exit 0). A usage error exits 2
and an internal error (the PDF cannot be read) exits 3; both write one line
to standard error and nothing to standard output.

pdftotext runs on a private copy of the bytes that were hashed, in a new
directory inside the PDF's own directory, so the verdict describes exactly
those bytes and nothing is written to the system temp directory. Reasons
may quote short excerpts of the document (at most 60 characters); they only
reach private diagnostics.
"""

import hashlib
import html
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile

TOOL = "verify_layout"
SCHEMA_VERSION = 1
SCRIPT_VERSION = "3"

PASS, FAIL, UNVERIFIED = "pass", "fail", "unverified"

EXTRACT_TIMEOUT = 25
VERSION_TIMEOUT = 10

# Distances from the paper edge to the template's text, measured on the
# compiled templates; re-measure them when a template's geometry changes.
# entry_gap is the largest allowed space above a résumé line, None for the
# letter, which has no entries.
PROFILES = {
    ("resume", "a4"): {"width": 595.28, "height": 841.89, "left": 59.5, "right": 59.5, "entry_gap": 24.0},
    ("resume", "letter"): {"width": 612.0, "height": 792.0, "left": 61.2, "right": 61.2, "entry_gap": 24.0},
    ("cover", "a4"): {"width": 595.28, "height": 841.89, "left": 79.4, "right": 79.4, "entry_gap": None},
    ("cover", "letter"): {"width": 612.0, "height": 792.0, "left": 79.4, "right": 79.4, "entry_gap": None},
}

SIZE_TOLERANCE = 1.5      # page size and page edge, in points
MARGIN_TOLERANCE = 14.0   # how far a word may reach past the profile's text edge
EDGE_BAND = 18.0          # no word this close to the top or bottom of the paper
FOOTER_BAND = 90.0        # lines starting this close to the bottom are not body
HOLE_LIMIT = 100.0
EARLY_END_SHARE = 0.25
THIN_FINAL_SHARE = 0.35
HEADING_RATIO = 1.25
SECTION_HEADING_RATIO = 2.0
INDENT = 8.0
OPENER_REACH = 60.0
SAME_ROW = 3.0
CORE_TRIM = 0.2
OVERLAP_SHARE = 0.30
PROBLEMS_PER_PAGE = 5
EXCERPT = 60

LIST_MARKERS = set("•‣▪●·")

CHECK_IDS = ["page-text", "page-size", "clipping", "text-overlap", "internal-hole",
             "early-page-end", "footer-collision", "final-page-thin", "orphaned-entry"]


class UsageError(Exception):
    pass


class InternalError(Exception):
    pass


def parse_arguments(argv):
    if not argv or argv[0].startswith("--"):
        raise UsageError("the first argument must be the PDF path")
    opts = {"pdf": argv[0]}
    names = {"--pdftotext": "pdftotext", "--template": "template", "--paper": "paper"}
    i = 1
    while i < len(argv):
        name = argv[i]
        if name not in names:
            raise UsageError("unknown argument")
        if names[name] in opts:
            raise UsageError(name + " given twice")
        if i + 1 >= len(argv):
            raise UsageError(name + " needs a value")
        opts[names[name]] = argv[i + 1]
        i += 2
    for name, key in names.items():
        if key not in opts:
            raise UsageError(name + " is required")
    if opts["template"] not in ("resume", "cover"):
        raise UsageError("--template must be resume or cover")
    if opts["paper"] not in ("a4", "letter"):
        raise UsageError("--paper must be a4 or letter")
    return opts


def excerpt(text):
    text = " ".join(text.split())
    return text if len(text) <= EXCERPT else text[:EXCERPT - 3] + "..."


class ExtractError(Exception):
    pass


def run_pdftotext(tool, data, pdf_path):
    """Returns the -bbox XHTML for data, or raises ExtractError naming why
    the extractor failed."""
    if not os.path.isabs(tool) or not os.path.isfile(tool):
        raise ExtractError("pdftotext is not an absolute path to a file")
    try:
        workdir = tempfile.mkdtemp(prefix=".layout-", dir=os.path.dirname(os.path.abspath(pdf_path)))
    except Exception as exc:
        raise ExtractError("no private copy could be made (%s)" % type(exc).__name__)
    try:
        copy = os.path.join(workdir, "copy.pdf")
        try:
            with open(copy, "wb") as fh:
                fh.write(data)
        except Exception as exc:
            raise ExtractError("no private copy could be made (%s)" % type(exc).__name__)
        try:
            done = subprocess.run([tool, "-bbox", "-enc", "UTF-8", copy, "-"],
                                  stdin=subprocess.DEVNULL, capture_output=True, timeout=EXTRACT_TIMEOUT)
        except subprocess.TimeoutExpired:
            raise ExtractError("pdftotext timed out after %d s" % EXTRACT_TIMEOUT)
        except Exception as exc:
            raise ExtractError("pdftotext could not start (%s)" % type(exc).__name__)
        if done.returncode != 0:
            raise ExtractError("pdftotext exited with status %d" % done.returncode)
        return done.stdout.decode("utf-8", errors="replace")
    finally:
        shutil.rmtree(workdir, ignore_errors=True)


def poppler_version(tool):
    try:
        done = subprocess.run([tool, "-v"], stdin=subprocess.DEVNULL, capture_output=True, timeout=VERSION_TIMEOUT)
    except Exception:
        return ""
    lines = done.stderr.decode("utf-8", errors="replace").splitlines()
    return lines[0].strip() if lines else ""


_PAGE = re.compile(r"<page\b([^>]*)>(.*?)</page>", re.S)
_WORD = re.compile(r"<word\b([^>]*)>(.*?)</word>", re.S)
_ATTR = re.compile(r'([A-Za-z]+)="([^"]*)"')


def parse_pages(xhtml):
    pages = []
    for page in _PAGE.finditer(xhtml):
        attrs = dict(_ATTR.findall(page.group(1)))
        words = []
        for word in _WORD.finditer(page.group(2)):
            box = dict(_ATTR.findall(word.group(1)))
            words.append({
                "x0": float(box["xMin"]), "y0": float(box["yMin"]),
                "x1": float(box["xMax"]), "y1": float(box["yMax"]),
                "text": html.unescape(word.group(2)),
            })
        pages.append({"width": float(attrs["width"]), "height": float(attrs["height"]), "words": words})
    return pages


def is_marker(word):
    return word["text"] != "" and all(ch in LIST_MARKERS for ch in word["text"])


def build_lines(words):
    rows = {}
    for word in words:
        rows.setdefault(round(word["y0"]), []).append(word)
    lines = []
    for row in rows.values():
        row = sorted(row, key=lambda w: (w["x0"], w["y0"], w["y1"], w["text"]))
        start = row[0]["x0"]
        lead = 0
        while lead < len(row) and is_marker(row[lead]):
            lead += 1
        if 0 < lead < len(row):
            start = row[lead]["x0"]
        text = " ".join(w["text"] for w in row)
        lines.append({
            "top": min(w["y0"] for w in row),
            "bottom": max(w["y1"] for w in row),
            "left": row[0]["x0"],
            "height": max(w["y1"] - w["y0"] for w in row),
            "text": text,
            "start": start,
            "has_text": re.search(r"\w", text) is not None,
        })
    lines.sort(key=lambda line: line["top"])
    return lines


def analyse(page):
    lines = build_lines(page["words"])
    limit = page["height"] - FOOTER_BAND
    body = [line for line in lines if line["top"] < limit]
    heights = sorted(line["height"] for line in body)
    page["lines"] = lines
    page["body"] = body
    page["empty"] = not body
    page["median"] = heights[len(heights) // 2] if heights else 0.0
    if body:
        lowest = max(line["bottom"] for line in body)
        page["bottom_space"] = page["height"] - lowest
        page["bottom_share"] = page["bottom_space"] / page["height"]
        tops = sorted(set(round(line["top"], 1) for line in body))
        gap, gap_y = 0.0, 0.0
        for upper, lower in zip(tops, tops[1:]):
            if lower - upper >= gap:
                gap, gap_y = lower - upper, upper
        page["gap"], page["gap_y"] = round(gap, 1), gap_y
    return page


def is_heading(line, median):
    return line["height"] > HEADING_RATIO * median


def is_section_heading(line, median):
    return is_heading(line, median) and line["height"] < SECTION_HEADING_RATIO * median


def text_start(lines):
    starts = [line["start"] for line in lines if line["has_text"]]
    return min(starts) if starts else None


def check(check_id, problems):
    if problems:
        return {"id": check_id, "status": FAIL, "reason": "; ".join(problems)}
    return {"id": check_id, "status": PASS, "reason": ""}


def check_page_text(pages):
    return ["p%d has no body text" % (n + 1) for n, p in enumerate(pages) if p["empty"]]


def check_page_size(pages, profile):
    out = []
    for n, p in enumerate(pages):
        if abs(p["width"] - profile["width"]) > SIZE_TOLERANCE or abs(p["height"] - profile["height"]) > SIZE_TOLERANCE:
            out.append("p%d is %.1f x %.1f pt, expected %.1f x %.1f" % (
                n + 1, p["width"], p["height"], profile["width"], profile["height"]))
    return out


def check_clipping(pages, profile):
    out = []
    for n, p in enumerate(pages):
        found = []
        right_edge = p["width"] - profile["right"]
        for w in p["words"]:
            if len(found) >= PROBLEMS_PER_PAGE:
                break
            where = None
            if (w["x0"] < -SIZE_TOLERANCE or w["y0"] < -SIZE_TOLERANCE
                    or w["x1"] > p["width"] + SIZE_TOLERANCE or w["y1"] > p["height"] + SIZE_TOLERANCE):
                where = "leaves the page"
            elif w["x0"] < profile["left"] - MARGIN_TOLERANCE:
                where = "starts in the left margin"
            elif w["x1"] > right_edge + MARGIN_TOLERANCE:
                where = "ends in the right margin"
            elif w["y0"] < EDGE_BAND:
                where = "is at the top edge"
            elif w["y1"] > p["height"] - EDGE_BAND:
                where = "is at the bottom edge"
            if where:
                found.append("p%d word %r %s" % (n + 1, excerpt(w["text"]), where))
        out.extend(found)
    return out


def core(word):
    trim = (word["y1"] - word["y0"]) * CORE_TRIM
    return word["x0"], word["y0"] + trim, word["x1"], word["y1"] - trim


def check_overlap(pages):
    out = []
    for n, p in enumerate(pages):
        cores = sorted(((core(w), w["text"]) for w in p["words"]), key=lambda c: c[0][1])
        found = []
        for i, ((ax0, ay0, ax1, ay1), atext) in enumerate(cores):
            if len(found) >= PROBLEMS_PER_PAGE:
                break
            area_a = (ax1 - ax0) * (ay1 - ay0)
            for (bx0, by0, bx1, by1), btext in cores[i + 1:]:
                if by0 >= ay1:
                    break
                area_b = (bx1 - bx0) * (by1 - by0)
                smaller = min(area_a, area_b)
                if smaller <= 0:
                    continue
                wide = min(ax1, bx1) - max(ax0, bx0)
                tall = min(ay1, by1) - max(ay0, by0)
                if wide > 0 and tall > 0 and wide * tall > OVERLAP_SHARE * smaller:
                    found.append("p%d words %r and %r overlap" % (n + 1, excerpt(atext), excerpt(btext)))
                    if len(found) >= PROBLEMS_PER_PAGE:
                        break
        out.extend(found)
    return out


def check_holes(pages):
    return ["p%d has a %.1f pt gap below y=%.1f" % (n + 1, p["gap"], p["gap_y"])
            for n, p in enumerate(pages) if not p["empty"] and p["gap"] > HOLE_LIMIT]


def check_early_end(pages):
    return ["p%d ends early, bottom share %.3f" % (n + 1, p["bottom_share"])
            for n, p in enumerate(pages[:-1]) if not p["empty"] and p["bottom_share"] > EARLY_END_SHARE]


def check_footer(pages):
    out = []
    for n, p in enumerate(pages):
        if p["empty"]:
            continue
        band = p["height"] - FOOTER_BAND
        tops = sorted(set(round(line["top"], 1) for line in p["lines"] if line["top"] >= band))
        if len(tops) > 1:
            out.append("p%d has %d lines in the footer band" % (n + 1, len(tops)))
    return out


def check_final_page(pages):
    last = pages[-1]
    if len(pages) > 1 and not last["empty"] and last["bottom_share"] > THIN_FINAL_SHARE:
        return ["p%d is thin, bottom share %.3f" % (len(pages), last["bottom_share"])]
    return []


def check_orphans(pages):
    body_lines = [line for p in pages for line in p["body"]]
    if not body_lines:
        return []
    margin = min(line["left"] for line in body_lines)

    def indented(line):
        return line["left"] > margin + INDENT

    out = []
    for n in range(len(pages) - 1):
        page, nxt = pages[n], pages[n + 1]
        if page["empty"] or nxt["empty"]:
            continue
        last, first = page["body"][-1], nxt["body"][0]
        if is_heading(last, page["median"]):
            out.append("p%d ends with the section heading %r" % (n + 1, excerpt(last["text"])))
            continue
        tail = [line for line in page["body"] if line["top"] >= last["top"] - SAME_ROW]
        tail_start = text_start(tail)
        if tail_start is None:
            if not indented(last) and indented(first):
                out.append("a list item is split across p%d and p%d" % (n + 1, n + 2))
            continue
        opener = next((line for line in nxt["body"]
                       if line["has_text"] and line["start"] <= margin + OPENER_REACH), None)
        if opener is None:
            continue
        head = [line for line in nxt["body"] if abs(line["top"] - opener["top"]) <= SAME_ROW]
        head_start = text_start(head)
        if head_start is not None and head_start > tail_start + INDENT:
            longest = max((line for line in tail if line["has_text"]), key=lambda line: len(line["text"]))
            out.append("entry header %r left on p%d, its content %r on p%d" % (
                excerpt(longest["text"]), n + 1, excerpt(opener["text"]), n + 2))
    return out


def check_entry_gaps(pages, limit):
    out = []
    met = False
    for n, p in enumerate(pages):
        reach = None
        for line in p["body"]:
            if not met:
                met = is_section_heading(line, p["median"])
            elif reach is not None and line["top"] - reach > limit:
                out.append("p%d has a %d pt gap above %r" % (n + 1, round(line["top"] - reach), excerpt(line["text"])))
            reach = line["bottom"] if reach is None else max(reach, line["bottom"])
    return out


def measurements(pages):
    out = []
    for n, p in enumerate(pages):
        row = {"page": n + 1, "height_pt": round(p["height"], 1), "empty": p["empty"]}
        if not p["empty"]:
            row.update({
                "text_top_pt": round(p["body"][0]["top"], 1),
                "text_bottom_pt": round(p["body"][-1]["bottom"], 1),
                "bottom_space_pt": round(p["bottom_space"], 1),
                "bottom_share": round(p["bottom_share"], 3),
                "largest_gap_pt": round(p["gap"], 1),
                "largest_gap_y_pt": round(p["gap_y"], 1),
            })
        out.append(row)
    return out


def overall(checks):
    statuses = [c["status"] for c in checks]
    if FAIL in statuses:
        return FAIL
    if not statuses or UNVERIFIED in statuses:
        return UNVERIFIED
    return PASS


def main(argv):
    opts = parse_arguments(argv)
    try:
        with open(opts["pdf"], "rb") as fh:
            data = fh.read()
    except Exception as exc:
        raise InternalError("cannot read the PDF (%s)" % type(exc).__name__)

    profile = PROFILES[(opts["template"], opts["paper"])]
    ids = CHECK_IDS + (["entry-gap"] if profile["entry_gap"] is not None else [])
    result = {
        "schema_version": SCHEMA_VERSION,
        "tool": TOOL,
        "script_version": SCRIPT_VERSION,
        "profile": "%s-%s" % (opts["template"], opts["paper"]),
        "pdf_sha256": hashlib.sha256(data).hexdigest(),
        "pdf_bytes": len(data),
        "python": "%d.%d.%d" % tuple(sys.version_info[:3]),
        "poppler": "",
        "pages": None,
        "measurements": [],
    }
    try:
        xhtml = run_pdftotext(opts["pdftotext"], data, opts["pdf"])
        pages = parse_pages(xhtml)
        if not pages:
            raise ExtractError("pdftotext output holds no page")
    except ExtractError as exc:
        checks = [{"id": i, "status": UNVERIFIED, "reason": str(exc)} for i in ids]
    except Exception as exc:
        reason = "pdftotext output could not be read (%s)" % type(exc).__name__
        checks = [{"id": i, "status": UNVERIFIED, "reason": reason} for i in ids]
    else:
        result["poppler"] = poppler_version(opts["pdftotext"])
        for page in pages:
            analyse(page)
        result["pages"] = len(pages)
        result["measurements"] = measurements(pages)
        checks = [
            check("page-text", check_page_text(pages)),
            check("page-size", check_page_size(pages, profile)),
            check("clipping", check_clipping(pages, profile)),
            check("text-overlap", check_overlap(pages)),
            check("internal-hole", check_holes(pages)),
            check("early-page-end", check_early_end(pages)),
            check("footer-collision", check_footer(pages)),
            check("final-page-thin", check_final_page(pages)),
            check("orphaned-entry", check_orphans(pages)),
        ]
        if profile["entry_gap"] is not None:
            checks.append(check("entry-gap", check_entry_gaps(pages, profile["entry_gap"])))
    result["checks"] = checks
    result["overall"] = overall(checks)
    sys.stdout.write(json.dumps(result, ensure_ascii=True, sort_keys=True) + "\n")
    sys.stdout.flush()


if __name__ == "__main__":
    try:
        main(sys.argv[1:])
    except UsageError as exc:
        sys.stderr.write("%s: usage error: %s\n" % (TOOL, exc))
        sys.exit(2)
    except InternalError as exc:
        sys.stderr.write("%s: %s\n" % (TOOL, exc))
        sys.exit(3)
    except Exception as exc:
        sys.stderr.write("%s: internal error (%s)\n" % (TOOL, type(exc).__name__))
        sys.exit(3)
    sys.exit(0)
