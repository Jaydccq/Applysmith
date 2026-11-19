"""Text-layer verifier for a compiled document PDF.

Run as  <python> -I - <pdf> [--pages N] [--max-pages N] [--min-chars N]
                         [--contains TEXT]... [--ascii-dates] [--dump-text PATH]
with this script on standard input. It reads the PDF once, extracts its text
layer with pypdf and prints one JSON verdict on standard output (exit 0).
A usage error exits 2 and an internal error (the PDF cannot be read, the
dump cannot be written) exits 3; both write one line to standard error and
nothing to standard output.

Only the standard library and pypdf are used. Reasons never quote document
text: they name problems by count, position or code point, because a
verdict may reach places the document itself should not.
"""

import hashlib
import io
import json
import logging
import os
import re
import sys
import unicodedata
import warnings

TOOL = "verify_pdf"
SCHEMA_VERSION = 1
SCRIPT_VERSION = "3"
FONT_USAGE_LIMIT = 64

PASS, FAIL, UNVERIFIED = "pass", "fail", "unverified"


class UsageError(Exception):
    pass


class InternalError(Exception):
    pass


def parse_arguments(argv):
    """Parse the fixed argument list by hand: no abbreviations, no help
    text, and an option value is taken verbatim even when it starts with a
    dash (a --contains value may)."""
    if not argv or argv[0].startswith("--"):
        raise UsageError("the first argument must be the PDF path")
    opts = {"pdf": argv[0], "pages": None, "max_pages": None, "min_chars": 1,
            "contains": [], "ascii_dates": False, "dump_text": None}
    seen = set()
    i = 1
    while i < len(argv):
        name = argv[i]
        if name == "--ascii-dates":
            if name in seen:
                raise UsageError("--ascii-dates given twice")
            seen.add(name)
            opts["ascii_dates"] = True
            i += 1
            continue
        if name not in ("--pages", "--max-pages", "--min-chars", "--contains", "--dump-text"):
            raise UsageError("unknown argument")
        if i + 1 >= len(argv):
            raise UsageError(name + " needs a value")
        value = argv[i + 1]
        i += 2
        if name == "--contains":
            opts["contains"].append(value)
            continue
        if name in seen:
            raise UsageError(name + " given twice")
        seen.add(name)
        if name == "--dump-text":
            if value == "":
                raise UsageError("--dump-text needs a path")
            opts["dump_text"] = value
            continue
        if not re.fullmatch(r"[0-9]{1,9}", value):
            raise UsageError(name + " needs a whole number")
        number = int(value)
        if name == "--min-chars":
            opts["min_chars"] = number
        elif number < 1:
            raise UsageError(name + " must be at least 1")
        else:
            opts["pages" if name == "--pages" else "max_pages"] = number
    return opts


_FOLD_TABLE = str.maketrans({
    "‘": "'", "’": "'",
    "“": '"', "”": '"',
    "–": "-", "—": "-",
    " ": " ",
})


def fold(text):
    """The comparison form: NFC, typographic quotes and dashes made ASCII,
    whitespace runs collapsed to one space, ends trimmed."""
    text = unicodedata.normalize("NFC", text).translate(_FOLD_TABLE)
    return re.sub(r"\s+", " ", text).strip()


_YEAR = r"(?:19|20)[0-9]{2}"
_DASH = "[‐-―−]"
_DASH_AFTER_YEAR = re.compile(r"(?<![0-9])" + _YEAR + r"[ \t]*(" + _DASH + ")")
_DASH_BEFORE_YEAR = re.compile("(" + _DASH + r")[ \t]*" + _YEAR + r"(?![0-9])")


def non_ascii_date_dashes(text):
    """Positions and characters of the dashes that touch a year. A dash
    between two years is one hit."""
    hits = {}
    for pattern in (_DASH_AFTER_YEAR, _DASH_BEFORE_YEAR):
        for match in pattern.finditer(text):
            hits[match.start(1)] = match.group(1)
    return [hits[pos] for pos in sorted(hits)]


def base_font_name(font):
    try:
        if font is None:
            return "unknown"
        name = font.get("/BaseFont")
        if name is None:
            return "unknown"
        name = str(name)
    except Exception:
        return "unknown"
    if name.startswith("/"):
        name = name[1:]
    name = re.sub(r"^[A-Z]{6}\+", "", name)
    return name or "unknown"


def collect_font_usage(reader):
    """Letters per (font, size), from the runs pypdf hands its extraction
    visitor."""
    counts = {}

    def visit(text, cm, tm, font, size):
        letters = sum(1 for ch in text if ch.isalpha())
        if not letters:
            return
        try:
            size_pt = round(float(size), 1) if size is not None else 0.0
        except (TypeError, ValueError):
            size_pt = 0.0
        key = (base_font_name(font), size_pt)
        counts[key] = counts.get(key, 0) + letters

    for page in reader.pages:
        page.extract_text(visitor_text=visit)
    rows = sorted(counts.items(), key=lambda kv: (-kv[1], kv[0][0], kv[0][1]))
    return [{"font": f, "size_pt": s, "letters": n} for (f, s), n in rows[:FONT_USAGE_LIMIT]]


def extract(data):
    """Returns (pypdf version or None, page texts or None, font usage or
    None, cause of failure)."""
    try:
        import pypdf
    except Exception as exc:
        return None, None, None, "pypdf could not be imported (%s)" % type(exc).__name__
    version = str(getattr(pypdf, "__version__", "")) or None
    try:
        reader = pypdf.PdfReader(io.BytesIO(data))
        texts = [page.extract_text() or "" for page in reader.pages]
    except Exception as exc:
        return version, None, None, "pypdf could not read the PDF (%s)" % type(exc).__name__
    try:
        usage = collect_font_usage(reader)
    except Exception:
        usage = None
    return version, texts, usage, ""


def check(check_id, problems):
    if problems:
        return {"id": check_id, "status": FAIL, "reason": "; ".join(problems)}
    return {"id": check_id, "status": PASS, "reason": ""}


def run_checks(opts, texts):
    layer = "\n".join(texts)
    folded = fold(layer)
    pages = len(texts)
    out = [check("pdf-readable", [])]
    problems = []
    if len(folded) < opts["min_chars"]:
        problems.append("text layer has %d characters, fewer than the required %d" % (len(folded), opts["min_chars"]))
    out.append(check("text-layer", problems))
    if opts["pages"] is not None:
        problems = []
        if pages != opts["pages"]:
            problems.append("%d pages, expected %d" % (pages, opts["pages"]))
        out.append(check("page-count", problems))
    if opts["max_pages"] is not None:
        problems = []
        if pages > opts["max_pages"]:
            problems.append("%d pages, more than the allowed %d" % (pages, opts["max_pages"]))
        out.append(check("max-pages", problems))
    if opts["contains"]:
        missing = ["#%d" % (n + 1) for n, want in enumerate(opts["contains"]) if fold(want) not in folded]
        out.append(check("required-text", ["required text missing: " + ", ".join(missing)] if missing else []))
    if opts["ascii_dates"]:
        dashes = non_ascii_date_dashes(layer)
        problems = []
        if dashes:
            problems.append("%d non-ASCII dash%s next to a year: %s" % (
                len(dashes), "" if len(dashes) == 1 else "es",
                ", ".join("U+%04X" % ord(d) for d in dashes)))
        out.append(check("ascii-dates", problems))
    return out


def requested_ids(opts):
    ids = ["pdf-readable", "text-layer"]
    if opts["pages"] is not None:
        ids.append("page-count")
    if opts["max_pages"] is not None:
        ids.append("max-pages")
    if opts["contains"]:
        ids.append("required-text")
    if opts["ascii_dates"]:
        ids.append("ascii-dates")
    return ids


def overall(checks):
    statuses = [c["status"] for c in checks]
    if FAIL in statuses:
        return FAIL
    if not statuses or UNVERIFIED in statuses:
        return UNVERIFIED
    return PASS


def dump_text(path, layer):
    try:
        parent = os.path.dirname(os.path.abspath(path))
        os.makedirs(parent, exist_ok=True)
        with open(path, "w", encoding="utf-8", newline="") as fh:
            fh.write(layer if layer.endswith("\n") else layer + "\n")
    except Exception as exc:
        raise InternalError("cannot write the text dump (%s)" % type(exc).__name__)


def main(argv):
    opts = parse_arguments(argv)
    try:
        with open(opts["pdf"], "rb") as fh:
            data = fh.read()
    except Exception as exc:
        raise InternalError("cannot read the PDF (%s)" % type(exc).__name__)

    version, texts, usage, cause = extract(data)
    result = {
        "schema_version": SCHEMA_VERSION,
        "tool": TOOL,
        "script_version": SCRIPT_VERSION,
        "pdf_sha256": hashlib.sha256(data).hexdigest(),
        "pdf_bytes": len(data),
        "python": "%d.%d.%d" % tuple(sys.version_info[:3]),
        "pypdf": version,
        "extractor": "pypdf",
        "pages": None,
        "text_sha256": None,
        "font_usage": None,
    }
    if texts is None:
        checks = [{"id": i, "status": UNVERIFIED, "reason": cause} for i in requested_ids(opts)]
    else:
        layer = "\n".join(texts)
        result["pages"] = len(texts)
        result["text_sha256"] = hashlib.sha256(layer.encode("utf-8")).hexdigest()
        result["font_usage"] = usage
        checks = run_checks(opts, texts)
        if opts["dump_text"] is not None:
            dump_text(opts["dump_text"], layer)
    result["checks"] = checks
    result["overall"] = overall(checks)
    sys.stdout.write(json.dumps(result, ensure_ascii=True, sort_keys=True) + "\n")
    sys.stdout.flush()


if __name__ == "__main__":
    logging.getLogger("pypdf").setLevel(logging.CRITICAL + 1)
    warnings.simplefilter("ignore")
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
