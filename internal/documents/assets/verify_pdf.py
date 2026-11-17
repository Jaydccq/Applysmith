#!/usr/bin/env python3
# Adapted from tools/verify_pdf.py of ai-job-search (MIT, Copyright (c) 2026
# Mads Lorentzen), https://github.com/MadsLorentzen/ai-job-search at commit
# a011214d60f943770e33f7d5f070e1a5f91e3822. Licence text:
# third_party/licenses/MIT-ai-job-search.txt; manifest:
# third_party/document-sources.json.
#
# Changes: the result is one schema-versioned JSON document on stdout instead
# of console text; every check is pass, fail or unverified, and a check that
# could not run is never a pass; pypdf is the only extractor (the Poppler
# fallback and the subprocess helpers are removed); the PDF is read once and
# the verdict carries the SHA-256 of exactly those bytes; required text is
# reported by index, not quoted; --max-pages is added; font_usage reports the
# letters set in each font and size so that a caller can hold a template to its
# fonts. normalize_text and find_non_ascii_date_ranges are unchanged.
"""Verify a PDF's page count and extractable text layer.

Contract (schema_version 1): the process prints one JSON object and exits 0
once a complete result was written; the verdict is in the document. Exit 2 is
a usage error and exit 3 an internal error, both without a result. Input is
the command line: the PDF path and the expectations.

`--contains` compares after `normalize_text()` has folded both sides:
whitespace, Unicode normalization form (NFC) and the typographic substitutions
LaTeX makes. `--ascii-dates` scans the raw layer for a year joined to a Unicode
dash and never folds.
"""

import argparse
import hashlib
import io
import json
import re
import sys
import unicodedata
from pathlib import Path

SCHEMA_VERSION = 1
SCRIPT_VERSION = "2"
TOOL = "verify_pdf"

PASS, FAIL, UNVERIFIED = "pass", "fail", "unverified"

# Typographic substitutions the templates produce from plain source text,
# mapped back to what a caller passes to --contains. Applied to both sides of
# the comparison; the extracted dump is never rewritten.
TYPOGRAPHIC_FOLDS = str.maketrans(
    {
        "\u2018": "'",
        "\u2019": "'",
        "\u201c": '"',
        "\u201d": '"',
        "\u2013": "-",
        "\u2014": "-",
        "\u00a0": " ",
    }
)


def normalize_text(text):
    """Fold a string for comparison: NFC, typographic punctuation, whitespace."""
    text = unicodedata.normalize("NFC", text).translate(TYPOGRAPHIC_FOLDS)
    return " ".join(text.split())


# Dash-like code points a year must never be joined to in a date range.
NON_ASCII_DASHES = "\u2010\u2011\u2012\u2013\u2014\u2015\u2212"
# A year is four digits standing alone.
_YEAR = r"(?<!\d)(?:19|20)\d{2}(?!\d)"
# Horizontal whitespace only between the year and the dash.
NON_ASCII_DATE_RANGE = re.compile(
    rf"{_YEAR}[^\S\n]*[{NON_ASCII_DASHES}]|[{NON_ASCII_DASHES}][^\S\n]*{_YEAR}"
)


def find_non_ascii_date_ranges(text):
    """Return (line, dash) for every year joined to a non-ASCII dash in raw text."""
    hits = []
    for match in NON_ASCII_DATE_RANGE.finditer(text):
        dash = next(c for c in match.group(0) if c in NON_ASCII_DASHES)
        line_start = text.rfind("\n", 0, match.start()) + 1
        line_end = text.find("\n", match.end())
        line = text[line_start : line_end if line_end != -1 else len(text)]
        hits.append((" ".join(line.split()), dash))
    return hits


# The most (font, size) pairs reported; a document uses a handful.
MAX_FONT_USAGE = 64
_SUBSET_PREFIX = re.compile(r"^[A-Z]{6}\+")


def font_usage(reader):
    """Count the letters of each font and size from the text runs; None if unreadable."""
    counts = {}

    def visit(text, _cm, _tm, font_dict, font_size):
        letters = sum(1 for c in text if c.isalpha())
        if not letters:
            return
        name = "unknown"
        if font_dict is not None and "/BaseFont" in font_dict:
            name = _SUBSET_PREFIX.sub("", str(font_dict["/BaseFont"]).lstrip("/"))
        key = (name, round(float(font_size or 0), 1))
        counts[key] = counts.get(key, 0) + letters

    try:
        for page in reader.pages:
            page.extract_text(visitor_text=visit)
    except Exception:  # pypdf raises many types on malformed input
        return None
    ranked = sorted(counts.items(), key=lambda kv: (-kv[1], kv[0]))[:MAX_FONT_USAGE]
    return [{"font": name, "size_pt": size, "letters": n} for (name, size), n in ranked]


def check(check_id, status, reason=""):
    return {"id": check_id, "status": status, "reason": reason}


def overall(checks):
    statuses = {c["status"] for c in checks}
    if FAIL in statuses:
        return FAIL
    if UNVERIFIED in statuses or not checks:
        return UNVERIFIED
    return PASS


def requested_checks(args):
    ids = ["pdf-readable", "text-layer"]
    if args.pages is not None:
        ids.append("page-count")
    if args.max_pages is not None:
        ids.append("max-pages")
    ids.extend(["required-text"] if args.contains else [])
    if args.ascii_dates:
        ids.append("ascii-dates")
    return ids


def verify(data, args):
    """Run the requested checks over the PDF bytes; returns (extras, checks, text)."""
    extras = {"extractor": "pypdf", "pages": None, "pypdf": None, "font_usage": None}
    try:
        import pypdf
    except ImportError as exc:
        reason = f"pypdf is not importable: {exc.__class__.__name__}"
        return extras, [check(i, UNVERIFIED, reason) for i in requested_checks(args)], None
    extras["pypdf"] = pypdf.__version__

    try:
        reader = pypdf.PdfReader(io.BytesIO(data))
        pages = len(reader.pages)
        text = "\n".join((page.extract_text() or "") for page in reader.pages)
    except Exception as exc:  # pypdf raises many types on malformed input
        reason = f"pypdf could not read the PDF: {exc.__class__.__name__}"
        return extras, [check(i, UNVERIFIED, reason) for i in requested_checks(args)], None
    extras["pages"] = pages
    extras["font_usage"] = font_usage(reader)
    checks = [check("pdf-readable", PASS)]

    normalized = normalize_text(text)
    if len(normalized) < args.min_chars:
        checks.append(
            check("text-layer", FAIL, f"text layer has {len(normalized)} character(s); expected at least {args.min_chars}")
        )
    else:
        checks.append(check("text-layer", PASS))

    if args.pages is not None:
        if pages != args.pages:
            checks.append(check("page-count", FAIL, f"expected {args.pages} page(s), found {pages}"))
        else:
            checks.append(check("page-count", PASS))
    if args.max_pages is not None:
        if pages > args.max_pages:
            checks.append(check("max-pages", FAIL, f"at most {args.max_pages} page(s) allowed, found {pages}"))
        else:
            checks.append(check("max-pages", PASS))

    if args.contains:
        missing = [str(i + 1) for i, req in enumerate(args.contains) if normalize_text(req) not in normalized]
        if missing:
            checks.append(check("required-text", FAIL, "required text missing: #" + ", #".join(missing)))
        else:
            checks.append(check("required-text", PASS))

    if args.ascii_dates:
        hits = find_non_ascii_date_ranges(text)
        if hits:
            listed = ", ".join(f"U+{ord(dash):04X}" for _line, dash in hits)
            checks.append(check("ascii-dates", FAIL, f"{len(hits)} date range(s) joined by a non-ASCII dash: {listed}"))
        else:
            checks.append(check("ascii-dates", PASS))
    return extras, checks, text


def build_parser():
    parser = argparse.ArgumentParser(description="Verify a PDF's page count and text layer; prints JSON.")
    parser.add_argument("pdf", type=Path, help="PDF file to verify")
    parser.add_argument("--pages", type=int, help="required exact page count")
    parser.add_argument("--max-pages", type=int, help="largest allowed page count")
    parser.add_argument("--min-chars", type=int, default=1, help="minimum text-layer characters (default: 1)")
    parser.add_argument(
        "--contains",
        action="append",
        default=[],
        help="text that must appear in the text layer, folded as described above; repeatable",
    )
    parser.add_argument("--dump-text", type=Path, help="write the extracted text layer to this path (UTF-8)")
    parser.add_argument("--ascii-dates", action="store_true", help="fail on a year joined to a Unicode dash")
    return parser


def main(argv=None):
    for stream in (sys.stdout, sys.stderr):
        reconfigure = getattr(stream, "reconfigure", None)
        if reconfigure:
            reconfigure(encoding="utf-8")
    args = build_parser().parse_args(argv)
    try:
        data = args.pdf.read_bytes()
    except OSError:
        print("verify_pdf: the PDF cannot be read", file=sys.stderr)
        return 3

    extras, checks, text = verify(data, args)
    if args.dump_text is not None and text is not None:
        try:
            args.dump_text.parent.mkdir(parents=True, exist_ok=True)
            args.dump_text.write_text(text if text.endswith("\n") else text + "\n", encoding="utf-8")
        except OSError:
            print("verify_pdf: the text dump cannot be written", file=sys.stderr)
            return 3

    result = {
        "schema_version": SCHEMA_VERSION,
        "tool": TOOL,
        "script_version": SCRIPT_VERSION,
        "pdf_sha256": hashlib.sha256(data).hexdigest(),
        "pdf_bytes": len(data),
        "python": ".".join(str(n) for n in sys.version_info[:3]),
        "text_sha256": hashlib.sha256(text.encode("utf-8")).hexdigest() if text is not None else None,
        **extras,
        "checks": checks,
        "overall": overall(checks),
    }
    json.dump(result, sys.stdout, ensure_ascii=True, sort_keys=True)
    sys.stdout.write("\n")
    return 0


if __name__ == "__main__":
    sys.exit(main())
