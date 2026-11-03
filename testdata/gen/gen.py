"""Writes testdata/golden/*.json from qobuz-dl 2.5.9 and its pinned dependencies.

    testdata/gen/.venv/bin/python -I testdata/gen/gen.py

Inputs are testdata/fixtures/: the recorded API answers, the string corpora and
the template matrices. Nothing here touches the network. The random corpus,
strings/random.txt, is written from a fixed seed on every run, so the whole
output is reproducible byte for byte.

Each golden is {"generator": ..., "cases": [{"name", "input", "output"} or
{"name", "input", "error"}]}, where error is the Python exception class name.
JSON can't tell 96 from 96.0, so every int, float and None gets a tag:
{"py": "int", "v": 96}, {"py": "float", "v": 96.0}, {"py": "none"}. The Go
loader refuses a plain JSON number or null as a generator bug.
"""

import json
import math
import random
import re
import sys
import textwrap
import unicodedata
from pathlib import Path
from types import SimpleNamespace

import pathvalidate
from pathvalidate import sanitize_filename, sanitize_filepath

from qobuz_dl import constants, downloader, utils
from qobuz_dl.downloader import Download
from qobuz_dl.qopy import Client

GENERATOR = "qobuz-dl 2.5.9"
ROOT = Path(__file__).resolve().parents[2]
FIXTURES = ROOT / "testdata" / "fixtures"
GOLDEN = ROOT / "testdata" / "golden"
SEED = 20261103
RANDOM_COUNT = 500


# --- Values ---------------------------------------------------------------


def tag(v):
    if isinstance(v, bool) or isinstance(v, str):
        return v
    if v is None:
        return {"py": "none"}
    if isinstance(v, int):
        return {"py": "int", "v": v}
    if isinstance(v, float):
        if math.isnan(v):
            return {"py": "float", "v": "nan"}
        if math.isinf(v):
            return {"py": "float", "v": "inf" if v > 0 else "-inf"}
        return {"py": "float", "v": v}
    if isinstance(v, (list, tuple)):
        return [tag(x) for x in v]
    if isinstance(v, dict):
        if "py" in v:
            raise ValueError(f"a dict with a 'py' key can't be told from a tagged value: {v!r}")
        return {k: tag(x) for k, x in v.items()}
    raise TypeError(f"no tag for {type(v).__name__}")


def untag(v):
    if isinstance(v, dict):
        if "py" in v:
            kind = v["py"]
            if kind == "none":
                return None
            if kind == "int":
                return int(v["v"])
            if kind == "float":
                return float(v["v"])
            raise ValueError(f"unknown tag {kind!r}")
        return {k: untag(x) for k, x in v.items()}
    if isinstance(v, list):
        return [untag(x) for x in v]
    return v


class Golden:
    def __init__(self, name):
        self.name = name
        self.cases = []
        self.names = set()

    def case(self, name, inp, fn):
        if name in self.names:
            raise ValueError(f"{self.name}: duplicate case {name!r}")
        self.names.add(name)
        try:
            out = fn()
        except Exception as e:
            self.cases.append({"name": name, "input": tag(inp), "error": type(e).__name__})
        else:
            self.cases.append({"name": name, "input": tag(inp), "output": tag(out)})

    def write(self):
        doc = {"generator": GENERATOR, "cases": self.cases}
        text = json.dumps(doc, ensure_ascii=False, indent=1, sort_keys=False) + "\n"
        (GOLDEN / f"{self.name}.json").write_text(text, encoding="utf-8")
        print(f"{self.name}.json: {len(self.cases)} cases")


# --- Corpora --------------------------------------------------------------

_ESCAPE = re.compile(r"\\(?:(\\)|u([0-9a-fA-F]{4})|U([0-9a-fA-F]{8})|(.?))")


def unescape(line):
    def sub(m):
        if m.group(4) is not None:
            raise ValueError(f"unknown escape \\{m.group(4)} in {line!r}: use \\uXXXX")
        if m.group(1):
            return "\\"
        return chr(int(m.group(2) or m.group(3), 16))

    return _ESCAPE.sub(sub, line)


def escape(s):
    out = []
    for i, c in enumerate(s):
        cat = unicodedata.category(c)
        if c == "\\":
            out.append("\\\\")
        elif (
            cat in ("Cc", "Cf", "Zl", "Zp", "Co", "Cn")
            or (cat == "Zs" and c != " ")
            or (c == " " and (i == 0 or i == len(s) - 1))
            or (c == "#" and i == 0)
        ):
            out.append(f"\\u{ord(c):04x}" if ord(c) <= 0xFFFF else f"\\U{ord(c):08x}")
        else:
            out.append(c)
    return "".join(out)


def read_corpus(name):
    lines = (FIXTURES / "strings" / f"{name}.txt").read_text(encoding="utf-8").split("\n")
    return [unescape(l) for l in lines if l and not l.startswith("#")]


def _assigned(lo, hi):
    return [chr(c) for c in range(lo, hi + 1) if unicodedata.category(chr(c)) != "Cn"]


# Pools and weights for the random corpus. The separators and brackets are the
# ones clean_filename touches, weighted up so its rules fire often.
POOLS = [
    (30, [c for c in map(chr, range(0x20, 0x7F))]),
    (25, list(" ,.:;|/\\_-()[]{}<>《》〈〉「」『』（）［］【】") + [" ", " ", ".", "_"]),
    (8, _assigned(0xA0, 0xFF)),
    (8, _assigned(0x400, 0x4FF)),
    (8, _assigned(0x4E00, 0x9FFF) + _assigned(0x3000, 0x303F)),
    (5, _assigned(0x600, 0x6FF)),
    (5, _assigned(0x300, 0x36F)),
    (5, _assigned(0x1F300, 0x1FAFF) + ["\u200d", "\ufe0f", "\U0001F3FD"]),
    (3, [chr(c) for c in list(range(0x00, 0x20)) + list(range(0x7F, 0xA0))]),
    (6, list("\t\n\r\x0b\x0c\x1c\x1d\x1e\x1f\x85\xa0\u1680\u2000\u2007\u200a\u200b\u2028\u2029\u202f\u205f\u3000")),
]


def random_corpus():
    rnd = random.Random(SEED)
    weights = [w for w, _ in POOLS]
    pools = [p for _, p in POOLS]
    out = []
    for i in range(RANDOM_COUNT):
        n = rnd.randint(100, 300) if i % 50 == 49 else rnd.randint(1, 40)
        s = "".join(rnd.choice(rnd.choices(pools, weights)[0]) for _ in range(n))
        out.append(s)
    return out


def write_random_corpus(strings):
    head = [
        f"# {RANDOM_COUNT} random strings from gen.py, seed {SEED}. Rewritten by every run of gen.py.",
        "# Escapes as in handpicked.txt.",
    ]
    body = [escape(s) for s in strings]
    text = "\n".join(head + body) + "\n"
    (FIXTURES / "strings" / "random.txt").write_text(text, encoding="utf-8")


# --- API fixtures ---------------------------------------------------------

_client = Client.__new__(Client)


def normalize(obj):
    return Client._normalize_json_strings(_client, obj)


def load_api():
    index = json.loads((FIXTURES / "api" / "index.json").read_text(encoding="utf-8"))
    raw = {}
    for r in index["requests"]:
        raw[r["file"]] = json.loads((FIXTURES / "api" / r["file"]).read_text(encoding="utf-8"))
    return index, raw


class FakeClient:
    """Answers get_track_url from the recorded getFileUrl answers."""

    def __init__(self, index, raw):
        self.urls = {}
        for r in index["requests"]:
            if r["endpoint"] == "track/getFileUrl":
                key = (str(r["params"]["id"]), int(r["params"]["format_id"]))
                self.urls[key] = normalize(raw[r["file"]])

    def get_track_url(self, id, fmt_id, force_segments=False):
        return self.urls[(str(id), int(fmt_id))]


def release_gate(fake, album, quality):
    me = SimpleNamespace(client=fake, quality=quality)
    fmt, _, bit_depth, sampling_rate = Download._get_format(me, album)
    return fmt, bit_depth, sampling_rate


def track_gate(fake, track, quality):
    me = SimpleNamespace(client=fake, quality=quality)
    fmt, _, bit_depth, sampling_rate = Download._get_format(me, track, is_track_id=True)
    return fmt, bit_depth, sampling_rate


def attribute_sets(index, raw):
    """Named attribute dicts, built by upstream's own builders from the fixtures."""
    fake = FakeClient(index, raw)
    items = index["items"]
    sets = []
    for kind, album_id in items["albums"].items():
        album = normalize(raw[f"album-get-{album_id}.json"])
        for quality in (27, 6, 5):
            fmt, bd, sr = release_gate(fake, album, quality)
            attrs = Download._get_album_attr(album, downloader._get_title(album), fmt, bd, sr)
            sets.append((f"album/{kind}/q{quality}", attrs, quality == 27))
        tracks = album["tracks"]["items"]
        picks = [tracks[0]] + [t for i, t in enumerate(tracks[1:], 1) if t.get("media_number") != tracks[i - 1].get("media_number")]
        for i, t in enumerate(picks):
            artist = downloader._safe_get(t, "performer", "name")
            attrs = Download._get_filename_attr(artist, t, album)
            sets.append((f"file/{kind}/{t['id']}", attrs, i == 0))

    track = normalize(raw[f"track-get-{items['track']}.json"])
    for quality in (27, 5):
        fmt, bd, sr = track_gate(fake, track, quality)
        attrs = Download._get_track_attr(track, downloader._get_title(track), bd, sr, fmt)
        sets.append((f"track/{items['track']}/q{quality}", attrs, quality == 27))
    artist = downloader._safe_get(track, "performer", "name")
    sets.append((f"file/track/{items['track']}", Download._get_filename_attr(artist, track, track["album"]), True))

    playlist = normalize(raw[f"playlist-get-{items['playlist']}-0.json"])
    for t in playlist["tracks"]["items"]:
        artist = downloader._safe_get(t, "performer", "name")
        sets.append((f"file/playlist/{t['id']}", Download._get_filename_attr(artist, t, t["album"]), False))
    return sets


def recorded_strings(index, raw):
    """Titles, names and versions from the fixtures, as the downloader sees them."""
    out = []

    def add(v):
        if isinstance(v, str) and v not in out:
            out.append(v)

    def walk(o):
        if isinstance(o, dict):
            for k in ("title", "name", "version", "work"):
                if k in o:
                    add(o[k])
            if "title" in o and o.get("version"):
                add(downloader._get_title(o))
            for v in o.values():
                walk(v)
        elif isinstance(o, list):
            for v in o:
                walk(v)

    for r in index["requests"]:
        if r["endpoint"] in ("album/get", "track/get", "playlist/get", "artist/get", "label/get"):
            walk(normalize(raw[r["file"]]))
    return out


def descriptions(index, raw):
    out = []
    for r in index["requests"]:
        if r["endpoint"] in ("album/get", "playlist/get", "artist/get", "label/get", "track/get"):
            body = normalize(raw[r["file"]])
            for d in (body.get("description"), body.get("biography", {}).get("content") if isinstance(body.get("biography"), dict) else None):
                if d and d not in out:
                    out.append(d)
    return out


# --- Goldens --------------------------------------------------------------


def gen_pystr():
    g = Golden("pystr")
    values = [
        0, 1, -1, 7, 96, 192, 2**31, -(2**31), 2**63 - 1, -(2**63),
        0.0, -0.0, 1.0, -1.0, 96.0, 192.0, 44.1, 88.2, 176.4, 352.8, 48.0, 22.05,
        0.1, 0.2, 0.30000000000000004, 1 / 3, 2 / 3, 0.5, 100.0, 123.456,
        1e-4, 0.0001234, 9.999e-5, 1e-5, 1.5e-5, 5e-324, 2.2250738585072014e-308,
        1e15, 999999999999999.9, 9999999999999998.0, 1e16, 1.5e16, 12345678901234567.0,
        123456789012345680.0, 1e21, 1e22, 1e100, 1.7976931348623157e308, -1e16, -1e-5,
        44100 / 1000, 96000 / 1000, 192000 / 1000, 88200 / 1000,
        math.inf, -math.inf, math.nan,
        None, True, False,
    ]
    seen = set()
    for v in values:
        name = f"{type(v).__name__} {v!r}"
        if name in seen:
            continue
        seen.add(name)
        g.case(name, v, lambda v=v: str(v))
    g.write()


def gen_format(attr_sets):
    g = Golden("format")
    matrix = untag(json.loads((FIXTURES / "templates" / "templates.json").read_text(encoding="utf-8")))
    matrix.pop("_comment")
    templates = matrix["templates"] + [
        constants.DEFAULT_FOLDER,
        constants.DEFAULT_TRACK,
        constants.DEFAULT_MULTIPLE_DISC_TRACK,
    ] + [t for pair in downloader.DEFAULT_FORMATS.values() for t in pair]
    templates = list(dict.fromkeys(templates))
    # The other sets differ from the full ones only in their values, so the
    # templates of the bot and the defaults are enough for them.
    short = [matrix["templates"][0], matrix["templates"][1], constants.DEFAULT_FOLDER,
             constants.DEFAULT_TRACK, constants.DEFAULT_MULTIPLE_DISC_TRACK]

    for set_name, attrs, full in attr_sets:
        every_key = "|".join(f"{{{k}}}" for k in attrs)
        for i, t in enumerate([every_key] + (templates if full else short)):
            name = f"{set_name} every key" if i == 0 else f"{set_name} {t}"
            g.case(name, {"template": t, "attrs": attrs}, lambda t=t, a=attrs: t.format(**a))

    values = matrix["values"]
    for spec in matrix["specs"]:
        for key in values:
            t = f"{{{key}:{spec}}}"
            g.case(f"spec {t}", {"template": t, "attrs": values}, lambda t=t: t.format(**values))
    for conv in matrix["conversions"]:
        for key in values:
            t = f"{{{key}{conv}}}"
            g.case(f"conversion {t}", {"template": t, "attrs": values}, lambda t=t: t.format(**values))
    for t in matrix["structure"]:
        g.case(f"structure {t!r}", {"template": t, "attrs": values}, lambda t=t: t.format(**values))
    g.write()


def corpus_all():
    seen = []
    for name in ("handpicked", "reserved", "random"):
        for i, s in enumerate(read_corpus(name), 1):
            seen.append((f"{name}:{i}", s))
    return [("empty", "")] + seen


def gen_strings(corpus):
    g = Golden("strings")
    for name, s in corpus:
        g.case(name, s, lambda s=s: {
            "lower": s.lower(),
            "upper": s.upper(),
            "casefold": s.casefold(),
            "title": s.title(),
            "capitalize": s.capitalize(),
            "strip": s.strip(),
            "strip_chars": s.strip(' ."-_\''),
            "lstrip_chars": s.lstrip(' ."-_\''),
            "rstrip_chars": s.rstrip(' ."-_\''),
            "isdigit": s.isdigit(),
            "len": len(s),
            "nfc": unicodedata.normalize("NFC", s),
        })
    g.write()


def gen_textwrap(corpus, descs):
    g = Golden("textwrap")
    inputs = []
    for d in descs:
        clean = re.sub(r"<[^<]+>", "", re.sub(r"<br\s*/?>", "\n", str(d)))
        inputs.append(("description raw", clean))
        for p in clean.split("\n"):
            if p.strip():
                inputs.append(("description paragraph", p.strip()))
    extra = [
        "x" * 150,
        "word " * 40,
        "a " + "y" * 69 + " b",
        "a " + "y" * 70 + " b",
        "a " + "y" * 71 + " b",
        "state-of-the-art " * 8,
        "re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re-re",
        "one--two--three " * 6,
        "em\u2014dash\u2014joined\u2014words " * 5,
        "tab\tseparated\tcolumns\tthat\tgo\ton\tfor\ta\twhile\tand\tthen\tsome\tmore\tand\tmore",
        "line one\nline two\nline three " * 4,
        "  leading and trailing whitespace that wraps around the seventy column limit  ",
        "Sentence one.  Sentence two.  Sentence three is a bit longer than the others are.",
        "https://www.example.com/a/very/long/url/that/does/not/have/any/spaces/in/it/at/all/ok",
        "1-2-3-4-5-6-7-8-9-10-11-12-13-14-15-16-17-18-19-20-21-22-23-24-25-26-27-28-29-30",
        "-leading-hyphen and a--double and trailing- hyphen-",
        "日本語の文章は空白がないので長い行はどこで折り返されるのでしょうかという疑問があります。" * 2,
        "Русский текст, который достаточно длинный, чтобы перенестись на следующую строку.",
        "\u00a0nbsp\u00a0inside\u00a0words\u00a0which\u00a0textwrap\u00a0treats\u00a0as\u00a0what\u00a0exactly?",
        "",
        " ",
        "short",
    ]
    inputs += [("extra", s) for s in extra]
    inputs += [(n, s) for n, s in corpus]
    seen = set()
    for i, (kind, s) in enumerate(inputs):
        if s in seen:
            continue
        seen.add(s)
        g.case(f"{kind} {i}", s, lambda s=s: textwrap.fill(s, width=70))
    g.write()


def ranges(pred):
    out = []
    start = None
    for c in range(0x110000 + 1):
        ok = c <= 0x10FFFF and not (0xD800 <= c <= 0xDFFF) and pred(chr(c))
        if ok and start is None:
            start = c
        elif not ok and start is not None:
            out.append(f"{start:04X}-{c - 1:04X}")
            start = None
    return out


def gen_regex():
    g = Golden("regex")
    for cls in (r"\w", r"\s", r"\d"):
        rx = re.compile(cls)
        g.case(cls, cls, lambda rx=rx: ranges(lambda ch: rx.fullmatch(ch) is not None))
    g.write()


def gen_clean_filename(corpus, recorded, attr_sets):
    g = Golden("clean_filename")
    inputs = list(corpus)
    inputs += [(f"recorded:{i}", s) for i, s in enumerate(recorded, 1)]
    bot_folder = "{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]"
    for set_name, attrs, _ in attr_sets:
        if set_name.startswith("album/"):
            inputs.append((f"folder:{set_name}", bot_folder.format(**attrs)))
            inputs.append((f"default folder:{set_name}", constants.DEFAULT_FOLDER.format(**attrs)))
    for name, s in inputs:
        for legacy in (False, True):
            g.case(f"{name} legacy={legacy}", {"name": s, "legacy_charmap": legacy},
                   lambda s=s, legacy=legacy: utils.clean_filename(s, legacy_charmap=legacy))
    g.write()


def long_names():
    return [
        ("ascii 300", "a" * 300),
        ("ascii 1000", "b" * 1000),
        ("ascii 254", "c" * 254),
        ("ascii 255", "c" * 255),
        ("ascii 256", "c" * 256),
        ("cyrillic 500", "Я" * 500),
        ("cyrillic 128", "ж" * 128),
        ("cjk 333", "音" * 333),
        ("cjk 86", "楽" * 86),
        ("emoji 250", "🎵" * 250),
        ("emoji 64", "🎶" * 64),
        ("mixed", ("Ab Яж 音楽 🎵 " * 40)[:600]),
        ("cut in a 2-byte char", "a" + "é" * 200),
        ("cut in a 3-byte char", "ab" + "音" * 200),
        ("cut in a 4-byte char", "abc" + "🎵" * 100),
        ("trailing dots at the cut", "x" * 250 + "....." + "y" * 10),
        ("spaces at the cut", "x" * 252 + "   " + "y" * 10),
        ("separators", ("Title - Part. One_ \"Two\" 'Three' " * 30)),
        ("decomposed", "e\u0301" * 200),
    ]


def gen_pathvalidate(corpus, recorded):
    g = Golden("pathvalidate")
    names = list(corpus) + list(long_names())
    names += [(f"recorded clean:{i}", utils.clean_filename(s)) for i, s in enumerate(recorded, 1)]
    paths = [
        "a/b", "/abs/path", "a//b", "a/./b", "a/../b", "../x", "./x", "x/", "/", "//", "C:\\x\\y",
        "C:", "C:/x", "a\\b", "CON/x", "x/NUL", "x/nul.txt", "a/ b /c", "a/b./c", "$MFT", "a/$MFT/b",
        "a/:/b", "a/*/b", "a/?/b", "dir/" + "z" * 300, "/".join(["d" * 50] * 10),
    ]
    for name, s in names:
        for repl in ("", "_"):
            g.case(f"sanitize_filename {name} repl={repl!r}", {"func": "sanitize_filename", "name": s, "replacement": repl},
                   lambda s=s, repl=repl: sanitize_filename(s, replacement_text=repl))
            g.case(f"sanitize_filepath {name} repl={repl!r}", {"func": "sanitize_filepath", "name": s, "replacement": repl},
                   lambda s=s, repl=repl: sanitize_filepath(s, replacement_text=repl))
    for i, p in enumerate(paths, 1):
        for repl in ("", "_"):
            g.case(f"sanitize_filepath path:{i} repl={repl!r}", {"func": "sanitize_filepath", "name": p, "replacement": repl},
                   lambda p=p, repl=repl: sanitize_filepath(p, replacement_text=repl))
    g.write()


def gen_url_info():
    g = Golden("url_info")
    for i, url in enumerate(read_corpus("urls"), 1):
        g.case(f"url:{i} {url!r}", url, lambda url=url: list(utils.get_url_info(url)))
    g.case("empty", "", lambda: list(utils.get_url_info("")))
    g.write()


def gen_normalize_json(index, raw):
    g = Golden("normalize_json")
    built = [
        ("plain string", "Hello"),
        ("ellipsis", "Wait..."),
        ("four dots", "Wait...."),
        ("six dots", "a......b"),
        ("ellipsis in a URL", "https://example.com/a...b"),
        ("ellipsis next to ://", "a... ://"),
        ("decomposed", "Fu\u0308r Elise"),
        ("decomposed with ellipsis", "Cafe\u0301..."),
        ("decomposed in a URL", "https://example.com/cafe\u0301..."),
        ("already NFC", "Café…"),
        ("Hangul jamo", "\u1100\u1161\u11a8"),
        ("empty", ""),
        ("int", 96),
        ("float", 44.1),
        ("whole float", 96.0),
        ("none", None),
        ("bool", True),
        ("list", ["a...", "e\u0301", 1, None, ["nested..."]]),
        ("dict", {"title": "Title...", "version": None, "n": 1, "x": 2.5, "url": "https://x/...", "tracks": {"items": [{"title": "e\u0301..."}]}}),
        ("dict key with ellipsis", {"key...": "value...", "ke\u0301y": "v"}),
        ("empty containers", {"a": [], "b": {}}),
    ]
    changed = []

    def walk(o):
        if isinstance(o, str):
            if normalize(o) != o and o not in changed:
                changed.append(o)
        elif isinstance(o, dict):
            for v in o.values():
                walk(v)
        elif isinstance(o, list):
            for v in o:
                walk(v)

    for r in index["requests"]:
        walk(raw[r["file"]])
    built += [(f"recorded:{i}", s) for i, s in enumerate(changed, 1)]
    album = index["items"]["albums"]["with_version"]
    built.append((f"album {album}", raw[f"album-get-{album}.json"]))
    for name, v in built:
        g.case(name, v, lambda v=v: normalize(v))
    g.write()


def gen_name_length(corpus):
    g = Golden("name_length")
    for name, s in corpus:
        if len(s.encode("utf-8")) < 12:
            continue
        for limit in (3, 4, 10, len(s.encode("utf-8")) - 1):
            g.case(f"{name} max={limit}", {"name": s, "max_bytes": limit},
                   lambda s=s, limit=limit: downloader._shorten_utf8(s, limit))
    for name, s in long_names():
        for limit in (0, 2, 3, 4, 5, 64, 100, 241, 255):
            g.case(f"{name} max={limit}", {"name": s, "max_bytes": limit},
                   lambda s=s, limit=limit: downloader._shorten_utf8(s, limit))
    g.write()


def main():
    if pathvalidate.__version__ != "3.3.1":
        sys.exit(f"pathvalidate {pathvalidate.__version__}: the goldens need 3.3.1")
    GOLDEN.mkdir(parents=True, exist_ok=True)
    generated = random_corpus()
    write_random_corpus(generated)
    if read_corpus("random") != generated:
        sys.exit("random.txt does not read back as written: fix escape()")

    index, raw = load_api()
    corpus = corpus_all()
    attr_sets = attribute_sets(index, raw)
    recorded = recorded_strings(index, raw)

    gen_pystr()
    gen_format(attr_sets)
    gen_strings(corpus)
    gen_textwrap(corpus, descriptions(index, raw))
    gen_regex()
    gen_clean_filename(corpus, recorded, attr_sets)
    gen_pathvalidate(corpus, recorded)
    gen_url_info()
    gen_normalize_json(index, raw)
    gen_name_length(corpus)


if __name__ == "__main__":
    main()
