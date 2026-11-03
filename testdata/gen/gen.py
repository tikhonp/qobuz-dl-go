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

import base64
import contextlib
import hashlib
import io
import json
import logging
import math
import random
import re
import sys
import textwrap
import unicodedata
import urllib.parse
from http import HTTPStatus
from pathlib import Path
from types import SimpleNamespace

import pathvalidate
import requests
from cryptography.hazmat.primitives import padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from pathvalidate import sanitize_filename, sanitize_filepath
from requests.adapters import BaseAdapter

from qobuz_dl import bundle as bundle_mod
from qobuz_dl import constants, downloader, qopy, utils
from qobuz_dl.settings import QobuzDLSettings
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


def _sigma_final(s):
    return s.lower()[-1] == "ς"


def _cased(c):
    # A cased character makes title() lowercase the next letter.
    return (c + "A").title()[-1] == "a"


def _case_ignorable(c):
    # Final sigma skips case-ignorable characters on both sides. A cased
    # character before the sigma would decide the result, so the test puts
    # cased ones after it.
    if _cased(c):
        return ("AΣ" + c).lower()[1] == "ς"
    return _sigma_final("A" + c + "Σ")


def char_map(fn):
    out = {}
    for c in range(0x110000):
        if 0xD800 <= c <= 0xDFFF:
            continue
        ch = chr(c)
        m = fn(ch)
        if m != ch:
            out[f"{c:04X}"] = m
    return out


def gen_unicode():
    g = Golden("unicode")
    g.case("unidata_version", "", lambda: unicodedata.unidata_version)
    sets = [
        ("assigned", lambda ch: unicodedata.category(ch) != "Cn"),
        ("isdigit", str.isdigit),
        ("isdecimal", str.isdecimal),
        ("isspace", str.isspace),
        ("isprintable", str.isprintable),
        ("cased", _cased),
        ("case_ignorable", _case_ignorable),
    ]
    for name, pred in sets:
        g.case(name, name, lambda pred=pred: ranges(pred))
    maps = [
        ("lower", str.lower),
        ("upper", str.upper),
        ("title", str.title),
        ("casefold", str.casefold),
    ]
    for name, fn in maps:
        g.case(f"{name} map", name, lambda fn=fn: char_map(fn))
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
        "\\\\server\\share\\x", "//server/share/x", "\\\\server", "\\\\?\\UNC\\s\\sh\\x",
        "a:b/c", "::", "*:x", "\u00e9:/x", " /x", "x/ ", "a/ /b", "a/./../../b", "a/CON./b", "a/ x /b",
        "\u00a0./x", "a/\u00a0./b", "/a/\u00a0./b", "\u2028.", "a/CON. /\u00a0./b", "a/" + "con." * 70 + "/\u00a0./b",
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


def album_dicts(index, raw):
    """Every album dict in the fixtures, as the downloader gets them."""
    out = []
    items = index["items"]
    for kind, album_id in items["albums"].items():
        out.append((f"album/{kind}", normalize(raw[f"album-get-{album_id}.json"])))
    track = normalize(raw[f"track-get-{items['track']}.json"])
    out.append((f"track/{items['track']}/album", track["album"]))
    for page_kind, file in (("playlist", f"playlist-get-{items['playlist']}-0.json"),):
        for t in normalize(raw[file])["tracks"]["items"]:
            out.append((f"{page_kind}/{t['id']}/album", t["album"]))
    for page_kind, file in (("artist", f"artist-get-{items['artist']}-0.json"), ("label", f"label-get-{items['label']}-0.json")):
        for a in normalize(raw[file])["albums"]["items"]:
            out.append((f"{page_kind}/{a['id']}", a))
    return out


def gen_album_artist(index, raw):
    g = Golden("album_artist")
    x = {"name": "X"}
    main = lambda name, *roles: {"name": name, "roles": ["main-artist", *roles]}
    built = [
        ("empty", {}),
        ("artist only", {"artist": x}),
        ("artists empty", {"artists": [], "artist": x}),
        ("artists None", {"artists": None, "artist": x}),
        ("several main artists", {"artists": [main("A"), {"name": "B", "roles": ["featured-artist"]}, main("C", "composer")], "artist": x}),
        ("one main artist", {"artists": [main("A")], "artist": x}),
        ("no main artist", {"artists": [{"name": "B", "roles": ["featured-artist"]}], "artist": x}),
        ("no main artist, no artist", {"artists": [{"name": "B", "roles": ["featured-artist"]}]}),
        ("roles missing", {"artists": [{"name": "B"}], "artist": x}),
        ("roles a string", {"artists": [{"name": "B", "roles": "main-artist, composer"}], "artist": x}),
        ("roles a dict", {"artists": [{"name": "B", "roles": {"main-artist": 1}}], "artist": x}),
        ("roles None", {"artists": [{"name": "B", "roles": None}], "artist": x}),
        ("roles a number", {"artists": [{"name": "B", "roles": 3}], "artist": x}),
        ("artists item a string", {"artists": ["main-artist"], "artist": x}),
        ("main artist without a name", {"artists": [main("A"), {"roles": ["main-artist"]}], "artist": x}),
        ("main artist name None", {"artists": [{"name": None, "roles": ["main-artist"]}], "artist": x}),
        ("main artist name a number", {"artists": [{"name": 7, "roles": ["main-artist"]}], "artist": x}),
        ("artists a number", {"artists": 5, "artist": x}),
        ("artists a string", {"artists": "abc", "artist": x}),
        ("artists a dict", {"artists": {"name": "A"}, "artist": x}),
        ("artist None", {"artist": None}),
        ("artist None after an error", {"artists": 5, "artist": None}),
        ("artist a string", {"artist": "X"}),
        ("artist name None", {"artist": {"name": None}}),
        ("artist name empty", {"artist": {"name": ""}}),
        ("artist name a number", {"artist": {"name": 5}}),
        ("artist name zero", {"artist": {"name": 0}}),
        ("artist without a name", {"artist": {"id": 1}}),
    ]
    for name, album in built + album_dicts(index, raw):
        g.case(name, album, lambda album=album: utils.get_album_artist(album))
    g.write()


def _sd_album(id, title, depth=24, rate=96.0, artist="Artist", version=None, **extra):
    a = {"id": id, "title": title, "version": version, "maximum_bit_depth": depth,
         "maximum_sampling_rate": rate, "artist": {"name": artist}}
    a.update(extra)
    return a


def _sd_page(items, name="Artist"):
    return {"name": name, "albums": {"items": items}}


def smart_discography_inputs(index, raw):
    A = _sd_album
    items = index["items"]
    yield "recorded artist", [normalize(raw[f"artist-get-{items['artist']}-0.json"])]
    yield "recorded label", [normalize(raw[f"label-get-{items['label']}-0.json"])]
    yield "grouping by essence", [_sd_page([
        A("a1", "Kind of Blue", 16, 44.1), A("a2", "Kind of Blue (Legacy Edition)", 24, 192),
        A("a3", "  KIND OF BLUE  ", 24, 96), A("a4", "Kind of Blue [Mono]", 24, 96),
        A("a5", "Kind of Blues", 24, 48)])]
    yield "leading parentheses", [_sd_page([
        A("b1", "(What's the Story) Morning Glory?", 16, 44.1),
        A("b2", "(What's The Story) Morning Glory? (Remastered)", 24, 96),
        A("b3", " (a) (b) Morning Glory?", 24, 48),
        A("b4", "(Live)", 24, 96), A("b5", "(Live", 24, 96), A("b6", "()", 24, 96), A("b7", "", 24, 96),
        A("b8", "(x)\n(y)\nTitle", 24, 96)])]
    yield "best depth then rate", [_sd_page([
        A("c1", "Album", 16, 192), A("c2", "Album", 24, 44.1), A("c3", "Album", 24, 96),
        A("c4", "Album", 24, 192), A("c5", "Album", 24, 48)])]
    yield "first of equals", [_sd_page([A("d1", "Same", 24, 96), A("d2", "Same", 24, 96.0), A("d3", "Same", 24.0, 96)])]
    yield "other artists dropped", [_sd_page([
        A("e1", "Collab", 24, 96, artist="Someone Else"), A("e2", "Collab", 16, 44.1),
        A("e3", "Solo", 24, 96, artist="artist"), A("e4", "Solo2", 24, 96)])]
    yield "remasters preferred", [_sd_page([
        A("f1", "Album", 24, 96), A("f2", "Album", 24, 96, version="Remastered 2011"),
        A("f3", "Hits", 24, 96), A("f4", "Hits (Remaster)", 24, 96),
        A("f5", "Songs", 24, 96, version="2009 Master"), A("f6", "Songs", 24, 96),
        A("f7", "Remastered Collection", 24, 96), A("f8", "MAſTERED", 24, 96)])]
    yield "remaster by another artist", [_sd_page([
        A("g1", "Album", 24, 96), A("g2", "Album", 24, 96, artist="Other", version="Remastered")])]
    yield "remaster at a lower quality", [_sd_page([
        A("h1", "Album", 24, 192), A("h2", "Album (Remastered)", 24, 96)])]
    yield "extras", [_sd_page([
        A("i1", "Album (Deluxe)", 24, 96), A("i2", "Live at Montreux", 24, 96),
        A("i3", "Songs", 24, 96, version="Collector's Edition"), A("i4", "Demos", 24, 96, version="Demo"),
        A("i5", "Tunes", 24, 96, version="Expanded"), A("i6", "Anniversary", 24, 96),
        A("i7", "Lıve at Abbey Road", 24, 96), A("i8", "LİVE", 24, 96), A("i9", "Oliver", 24, 96),
        A("i10", "Delivered", 24, 96), A("i11", "Studio", 24, 96)])]
    yield "extra and plain in a group", [_sd_page([
        A("j1", "Album", 24, 96, version="Deluxe"), A("j2", "Album", 24, 96)])]
    yield "version None and numbers", [_sd_page([
        A("k1", "None", 24, 96), A("k2", "Title", 24, 96, version=0), A("k3", "Title2", 24, 96, version=2011.0)])]
    yield "two pages", [_sd_page([A("l1", "One", 24, 96)]), _sd_page([A("l2", "Two", 24, 96), A("l3", "One", 24, 192)], name="Other")]
    yield "empty page", [_sd_page([])]
    yield "no pages", []
    yield "no name", [{"albums": {"items": []}}]
    yield "albums None", [{"name": "Artist", "albums": None}]
    yield "items missing", [{"name": "Artist", "albums": {}}]
    yield "item a string", [_sd_page(["x"])]
    yield "title missing", [_sd_page([{"id": "m1", "maximum_bit_depth": 24}])]
    yield "title a number", [_sd_page([A("n1", 5)])]
    yield "depth missing", [_sd_page([{"id": "o1", "title": "T", "maximum_sampling_rate": 96, "artist": {"name": "Artist"}}])]
    yield "depth None alone", [_sd_page([A("p1", "T", None, 96)])]
    yield "depth None and int", [_sd_page([A("p2", "T", None, 96), A("p3", "T", 24, 96)])]
    yield "depth bool", [_sd_page([A("p4", "T", True, 96), A("p5", "T", 1, 48)])]
    yield "rate missing on a worse album", [_sd_page([A("q1", "T", 24, 96), {"id": "q2", "title": "T", "maximum_bit_depth": 16}])]
    yield "artist missing on a best album", [_sd_page([A("r1", "T", 24, 96), {"id": "r2", "title": "T", "maximum_bit_depth": 24, "maximum_sampling_rate": 96}])]
    yield "artist None", [_sd_page([{"id": "s1", "title": "T", "maximum_bit_depth": 24, "maximum_sampling_rate": 96, "artist": None}])]
    yield "big ints against floats", [_sd_page([
        A("t1", "T", 9007199254740993, 96), A("t2", "T", 9007199254740992.0, 96)])]
    yield "requested name a number", [_sd_page([A("u1", "T", 24, 96, artist=5)], name=5.0)]


def gen_smart_discography(index, raw):
    g = Golden("smart_discography")
    for name, pages in smart_discography_inputs(index, raw):
        for save_space in (True, False):
            for skip_extras in (True, False):
                g.case(f"{name} save_space={save_space} skip_extras={skip_extras}",
                       {"pages": pages, "save_space": save_space, "skip_extras": skip_extras},
                       lambda p=pages, s=save_space, e=skip_extras: [a["id"] for a in utils.smart_discography_filter(p, save_space=s, skip_extras=e)])
    g.write()


def gen_release_type(index, raw):
    g = Golden("release_type")
    values = [None, "", "ep", "EP", "Ep", "single", "album", "compilation", "epmini", "live album",
              "best-of", "ÉP", "ǆ", "ﬁ", "ß", "İ", 0, 5, 0.0, True, False]
    for _, album in album_dicts(index, raw):
        for k in ("release_type", "product_type"):
            v = album.get(k)
            if v is not None and v not in values:
                values.append(v)
    for v in values:
        g.case(f"{type(v).__name__} {v!r}", v, lambda v=v: downloader.format_release_type(v))
    g.write()


def gen_settings():
    g = Golden("settings")

    def attrs():
        out = {}
        for k, v in vars(QobuzDLSettings()).items():
            out[k] = sorted(v) if isinstance(v, set) else v
        return out

    g.case("defaults", {}, attrs)
    g.write()


# --- Bundle and client ----------------------------------------------------

# The synthetic bundle carries the app ID and secret qobuzdltest will use, so
# the fake of step 6 can serve these files as they are.
FAKE_APP_ID = "123456789"
FAKE_SECRET = "5e1f0c2a9b7d4e3f8a6c1b0d2e4f6a8c"
BAD_SECRET = "0f9e8d7c6b5a49382716051423324150"
FAKE_TOKEN = "fake-user-auth-token-for-tests-" + "x" * 40
# Midnight UTC on 2000-01-01, where testing/synctest's clock starts, so the Go
# tests sign with the same timestamp.
FIXED_TS = 946684800
BUNDLE_PATH = "/resources/8.1.0-b019/bundle.js"
API_BASE = "https://www.qobuz.com/api.json/0.2/"
PLAY_BASE = "https://play.qobuz.com"
FILLER = "Zm9yIHRoZSBzaWduYXR1cmUgdGhhdCBuZXZlciBpcyBjaGVja2Vk"[:44]


def _secret_parts(secret):
    full = base64.standard_b64encode(secret.encode()).decode() + FILLER
    return full[:17], full[17:39], full[39:]


def bundle_text():
    b_seed, b_info, b_extras = _secret_parts(BAD_SECRET)
    l_seed, l_info, l_extras = _secret_parts(FAKE_SECRET)
    return "".join([
        '/*! synthetic bundle for qobuz-dl-go tests */\n',
        '!function(){var n={};n.config={development:{api:{appId:"000000000",appSecret:"',
        "d" * 32, '"}},production:{api:{appId:"', FAKE_APP_ID, '",appSecret:"', "e" * 32, '"}}};\n',
        # london first with a seed a later line replaces, then berlin, which
        # moves to the front, then abidjan, whose short seed decodes to "".
        'var l=new s;l.initialSeed("bm90IHRoaXMgb25l",window.utimezone.london),\n',
        'b.initialSeed("', b_seed, '",window.utimezone.berlin),\n',
        'c.initialSeed("c2hvcnQ=",window.utimezone.abidjan),\n',
        'l.initialSeed("', l_seed, '",window.utimezone.london);\n',
        'var z=[{offset:"GMT+01:00",name:"Europe/Berlin",info:"', b_info, '",extras:"', b_extras, '"},\n',
        '{offset:"GMT",name:"Europe/London",info:"', l_info, '",extras:"', l_extras, '"},\n',
        '{offset:"GMT-05:00",name:"America/Montreal",info:"aWdub3JlZA==",extras:"aWdub3JlZA=="}];\n',
        '}();\n',
    ])


def login_html():
    return (
        '<!DOCTYPE html>\n<html lang="en">\n<head>\n<meta charset="utf-8">\n<title>Qobuz Web Player</title>\n'
        '<script src="/resources/8.1.0-b019/vendor.js"></script>\n'
        f'<script src="{BUNDLE_PATH}"></script>\n</head>\n<body></body>\n</html>\n'
    )


def write_bundle_fixtures():
    d = FIXTURES / "bundle"
    d.mkdir(exist_ok=True)
    (d / "login.html").write_text(login_html(), encoding="utf-8")
    (d / "bundle.js").write_text(bundle_text(), encoding="utf-8")


def _bundle(text):
    b = bundle_mod.Bundle.__new__(bundle_mod.Bundle)
    b._bundle = text
    return b


def gen_secrets():
    g = Golden("secrets")
    text = bundle_text()
    g.case("fixture bundle app ID", {"bundle": "bundle.js"}, lambda: _bundle(text).get_app_id())
    g.case("fixture bundle secrets", {"bundle": "bundle.js"},
           lambda: [[k, v] for k, v in _bundle(text).get_secrets().items()])
    seed = 'a.initialSeed("{s}",window.utimezone.{tz})'
    built = [
        ("one timezone", seed.format(s="QUJD", tz="paris")),
        ("no timezone", "nothing here"),
        ("two timezones without info", seed.format(s="QUJD" + FILLER, tz="paris") + seed.format(s="QUFB" + FILLER, tz="tokyo")),
        ("invalid UTF-8", seed.format(s="wyg=" + FILLER, tz="paris") + seed.format(s="QUJD" + FILLER, tz="tokyo")),
        ("bad padding", seed.format(s="QUJDR" + FILLER, tz="paris") + seed.format(s="QUJD" + FILLER, tz="tokyo")),
        ("info for a prefix timezone", seed.format(s="QU", tz="lon") + seed.format(s="QU", tz="london")
         + 'name:"Europe/London",info:"JD",extras:"' + FILLER + '"'
         + 'name:"Asia/Lon",info:"FBQU",extras:"FB' + FILLER + '"'),
        ("underscores are dropped", seed.format(s="Q_UJD" + FILLER, tz="paris") + seed.format(s="QU_FB" + FILLER, tz="tokyo")),
    ]
    for name, t in built:
        g.case(f"secrets: {name}", {"text": t},
               lambda t=t: [[k, v] for k, v in _bundle(t).get_secrets().items()])
    app_ids = [
        ("eight digits", 'production:{api:{appId:"12345678",appSecret:"' + "e" * 32 + '"'),
        ("short secret", 'production:{api:{appId:"123456789",appSecret:"' + "e" * 31 + '"'),
        ("first of two", 'production:{api:{appId:"111111111",appSecret:"' + "e" * 32 + '"production:{api:{appId:"222222222",appSecret:"' + "f" * 32 + '"'),
        ("arabic-indic digits", 'production:{api:{appId:"١٢٣٤٥٦٧٨٩",appSecret:"' + "e" * 32 + '"'),
        ("none", "development:{}"),
    ]
    for name, t in app_ids:
        g.case(f"app ID: {name}", {"text": t}, lambda t=t: _bundle(t).get_app_id())
    pages = [
        ("fixture login page", login_html()),
        ("no script", "<html></html>"),
        ("single quotes", "<script src='/resources/8.1.0-b019/bundle.js'></script>"),
        ("two letters in the build", '<script src="/resources/8.1.0-bb19/bundle.js"></script>'),
        ("first of two", '<script src="/resources/1.2.3-a001/bundle.js"></script><script src="/resources/4.5.6-b002/bundle.js"></script>'),
    ]
    for name, page in pages:
        g.case(f"login page: {name}", {"page": page}, lambda page=page: (lambda m: m.group(1) if m else None)(bundle_mod._BUNDLE_URL_REGEX.search(page)))
    rnd = random.Random(SEED + 1)
    for i in range(400):
        s = "".join(rnd.choice("QUJDRA=_x-+/") for _ in range(rnd.randint(0, 14)))
        g.case(f"b64 standard:{i} {s!r}", {"s": s}, lambda s=s: base64.standard_b64decode(s).hex())
        g.case(f"b64 urlsafe:{i} {s!r}", {"s": s}, lambda s=s: Client._b64url_decode(s).hex())
    g.case("b64 urlsafe: non-ASCII", {"s": "QUJDé"}, lambda: Client._b64url_decode("QUJDé").hex())
    g.write()


def gen_signatures():
    g = Golden("signatures")
    for track_id, fmt_id, ts, secret in [
        ("5966783", 5, FIXED_TS, FAKE_SECRET),
        ("20273753", 27, 1762185600, FAKE_SECRET),
        ("20273753", 6, 1762185600, BAD_SECRET),
        ("115851707", 7, 0, ""),
    ]:
        def sig(track_id=track_id, fmt_id=fmt_id, ts=ts, secret=secret):
            with offline(lambda req: (200, "{}"), ts=ts) as rec:
                me = Client.__new__(Client)
                me.base, me.sec, me.session = API_BASE, None, requests.Session()
                me.api_call("track/getFileUrl", id=track_id, fmt_id=fmt_id, sec=secret)
            q = urllib.parse.parse_qs(urllib.parse.urlsplit(rec.log[0][0].url).query)
            return q["request_sig"][0]
        g.case(f"getFileUrl {track_id} {fmt_id} {ts}", {"track_id": track_id, "fmt_id": fmt_id, "ts": ts, "secret": secret}, sig)
    modern = [
        ("session/start", {"profile": "qbz-1"}),
        ("file/url", {"track_id": "20273753", "format_id": 27, "intent": "import"}),
        ("file/url", {"track_id": 20273753, "format_id": "7", "intent": "import"}),
        ("track/lyricsUrl", {"track_id": "1", "language": "fr"}),
        ("a/b", {"z": "1", "a": 2.5, "m": True, "b": None, "c": [1, 2], "d": {"k": "v"}, "e": 96.0, "é": "x", "B": "y"}),
        ("a/b", {}),
    ]
    for i, (epoint, params) in enumerate(modern):
        for ts, secret in [(FIXED_TS, FAKE_SECRET), (1762185600, "")]:
            p = dict(params)
            p["request_ts"] = ts
            p["request_sig"] = "ignored"
            g.case(f"modern {i} {epoint} {ts}", {"epoint": epoint, "params": p, "secret": secret},
                   lambda epoint=epoint, p=p, secret=secret: Client._modern_sig(_client, epoint, p, secret))
    g.write()


def _b64url(b):
    return base64.urlsafe_b64encode(b).decode().rstrip("=")


def session_infos(n):
    h = hashlib.sha256(f"infos {n}".encode()).digest()
    return _b64url(h[:16]) + "." + _b64url(h[16:] + b"qbz")


def wrap_key(session_key, raw_key, iv):
    p = padding.PKCS7(128).padder()
    padded = p.update(raw_key) + p.finalize()
    e = Cipher(algorithms.AES(session_key), modes.CBC(iv)).encryptor()
    return "qbz-1." + _b64url(e.update(padded) + e.finalize()) + "." + _b64url(iv)


def derive(secret, infos):
    me = SimpleNamespace(session_infos=infos, sec=secret, _b64url_decode=Client._b64url_decode)
    return Client._derive_session_key(me)


def unwrap(session_key, key):
    me = SimpleNamespace(session_key=session_key, _b64url_decode=Client._b64url_decode)
    return Client._unwrap_track_key(me, key)


def gen_session_key():
    g = Golden("session_key")
    derives = [(FAKE_SECRET, session_infos(1)), (BAD_SECRET, session_infos(2)), ("", session_infos(3)),
               (FAKE_SECRET, "." + _b64url(b"info")), (FAKE_SECRET, _b64url(b"salt") + "."),
               ("5e 1f 0c 2a 9b 7d 4e 3f 8a 6c 1b 0d 2e 4f 6a 8c", session_infos(1)),
               (FAKE_SECRET.upper(), session_infos(1)),
               (FAKE_SECRET, "no-dot"), (FAKE_SECRET, "a.b.c"), (FAKE_SECRET, "QUJDR.QUJD"),
               ("5e1", session_infos(1)), ("5e 1f0", session_infos(1)), ("zz", session_infos(1))]
    for i, (secret, infos) in enumerate(derives):
        g.case(f"derive {i}", {"secret": secret, "infos": infos}, lambda s=secret, i=infos: derive(s, i).hex())
    sk = derive(FAKE_SECRET, session_infos(1))
    other = derive(BAD_SECRET, session_infos(2))
    unwraps = []
    for n in (16, 15, 32, 1):
        raw = hashlib.sha256(f"raw {n}".encode()).digest()[:n]
        iv = hashlib.md5(f"iv {n}".encode()).digest()
        unwraps.append((f"raw key of {n} bytes", sk, wrap_key(sk, raw, iv)))
    good = wrap_key(sk, hashlib.sha256(b"raw 16").digest()[:16], hashlib.md5(b"iv 16").digest())
    _, w, v = good.split(".")
    unwraps += [
        ("wrong session key", other, good),
        ("two parts", sk, "qbz-1." + w),
        ("four parts", sk, good + ".x"),
        ("short IV", sk, f"qbz-1.{w}.{_b64url(b'short')}"),
        ("wrapped not a block multiple", sk, f"qbz-1.{w[:-3]}.{v}"),
        ("empty wrapped", sk, f"qbz-1..{v}"),
        ("bad base64", sk, f"qbz-1.{w}Q.{v}"),
    ]
    for name, key, token in unwraps:
        g.case(f"unwrap: {name}", {"session_key": key.hex(), "key": token}, lambda k=key, t=token: unwrap(k, t).hex())
    g.write()


class Recorder(BaseAdapter):
    """A requests transport that answers from a function and logs each exchange."""

    def __init__(self, answer):
        super().__init__()
        self.answer = answer
        self.log = []

    def send(self, request, **kwargs):
        status, body = self.answer(request)
        self.log.append((request, status, body))
        r = requests.Response()
        r.status_code = status
        r.reason = HTTPStatus(status).phrase
        r._content = body.encode("utf-8")
        r.encoding = "utf-8"
        r.headers = requests.structures.CaseInsensitiveDict({"Content-Type": "application/json"})
        r.url = request.url
        r.request = request
        return r

    def close(self):
        pass


@contextlib.contextmanager
def offline(answer, ts=FIXED_TS):
    rec = Recorder(answer)

    class Session(requests.Session):
        def __init__(self):
            super().__init__()
            self.mount("https://", rec)
            self.mount("http://", rec)

    saved = requests.Session, bundle_mod.Session, qopy.time
    requests.Session = bundle_mod.Session = Session
    # A fraction of a second checks that the port drops it as int() does.
    qopy.time = SimpleNamespace(time=lambda: ts + 0.75)
    try:
        yield rec
    finally:
        requests.Session, bundle_mod.Session, qopy.time = saved


def exchanges(rec):
    out = []
    for req, status, body in rec.log:
        out.append({
            "request": {
                "method": req.method,
                "url": req.url,
                "headers": [[k, v] for k, v in req.headers.items()],
                "body": req.body if isinstance(req.body, str) or req.body is None else req.body.decode(),
            },
            "response": {"status": status, "body": body},
        })
    return out


def file_url_answer(q):
    sk = derive(FAKE_SECRET, session_infos(1))
    return {"track_id": int(q["track_id"]), "format_id": int(q["format_id"]), "mime_type": "audio/mp4; codecs=\"flac\"",
            "sampling_rate": 96000, "bits_depth": 24, "n_segments": 3,
            "url_template": "https://cdn.test/file/$SEGMENT$?sig=abc",
            "key": wrap_key(sk, hashlib.sha256(b"raw 16").digest()[:16], hashlib.md5(b"iv 16").digest()),
            "restrictions": []}


def api_answer(overrides):
    """The default API. It checks signatures with FAKE_SECRET and gives small answers."""

    def answer(req):
        u = urllib.parse.urlsplit(req.url)
        if u.netloc == "play.qobuz.com":
            if "play" in overrides:
                return overrides["play"](u.path)
            if u.path == "/login":
                return 200, login_html()
            if u.path == BUNDLE_PATH:
                return 200, bundle_text()
            return 404, "not found"
        ep = u.path.removeprefix("/api.json/0.2/")
        q = dict(urllib.parse.parse_qsl(u.query or req.body or "", keep_blank_values=True))
        if ep in overrides:
            r = overrides[ep](q)
            if r is not None:
                return r
        if ep == "user/get":
            return 200, json.dumps({"id": 2468, "credential": {"parameters": {"short_label": "Studio Premier"}}})
        if ep == "track/getFileUrl":
            want = hashlib.md5(f"trackgetFileUrlformat_id{q['format_id']}intentstreamtrack_id{q['track_id']}{q['request_ts']}{FAKE_SECRET}".encode()).hexdigest()
            if q["request_sig"] != want:
                return 400, json.dumps({"status": "error", "code": 400, "message": "Invalid Request Signature parameter (request_sig)"})
            fmt = int(q["format_id"])
            return 200, json.dumps({"track_id": int(q["track_id"]), "duration": 30, "url": f"https://cdn.test/file/{q['track_id']}/{fmt}?sig=abc...def",
                                    "format_id": fmt, "mime_type": "audio/mpeg" if fmt == 5 else "audio/flac",
                                    "restrictions": [], "sampling_rate": 44.1, "bit_depth": 16, "blob": "signed"})
        if ep == "session/start":
            return 200, json.dumps({"session_id": "session-1", "infos": session_infos(1), "expires_at": FIXED_TS + 3600})
        if ep == "file/url":
            return 200, json.dumps(file_url_answer(q))
        if ep == "album/get":
            return 200, json.dumps({"id": q["album_id"], "title": "Album...", "version": None, "artist": {"id": 1, "name": "Café"}})
        if ep == "track/get":
            return 200, json.dumps({"id": int(q["track_id"]), "title": "Track", "album": {"id": "a1", "title": "Album"}})
        if ep in ("playlist/get", "artist/get", "label/get"):
            key = "tracks" if ep == "playlist/get" else "albums"
            off = int(q["offset"])
            n = max(0, min(50, 70 - off))
            return 200, json.dumps({"id": 9, "name": "Paged", key: {"offset": off, "limit": 50, "total": 70,
                                                                     "items": [{"id": off + i} for i in range(n)]}})
        if ep == "catalog/search":
            t = q["type"]
            return 200, json.dumps({"query": q["query"], t: {"limit": int(q["limit"]), "offset": int(q.get("offset", 0)),
                                                             "total": 1, "items": [{"id": 1, "title": "Found..."}]}})
        return 404, json.dumps({"status": "error", "code": 404, "message": "No route"})

    return answer


CLIENT_DEFAULTS = {"email": "", "pwd": "", "app_id": FAKE_APP_ID, "secrets": [BAD_SECRET, FAKE_SECRET],
                   "token": FAKE_TOKEN, "force_english": True}


def client_state(c):
    return {"app_id": c.id, "secrets": list(c.secrets), "sec": c.sec,
            "label": getattr(c, "label", None), "user_id": getattr(c, "user_id", None)}


def run_client(opts, overrides, call):
    o = dict(CLIENT_DEFAULTS, **opts)
    out = {}
    # Upstream prints and logs as it goes; the golden needs none of it.
    with offline(api_answer(overrides)) as rec, contextlib.redirect_stdout(io.StringIO()):
        try:
            c = Client(o["email"], o["pwd"], o["app_id"], o["secrets"], o["token"], force_english=o["force_english"])
            out["state"] = client_state(c)
            if call:
                fn, args = call[0], call[1:]
                if fn == "api_call":
                    res = c.api_call(args[0], **args[1])
                else:
                    res = getattr(c, fn)(*args)
                if fn.startswith("get_") and fn.endswith("_meta") and fn not in ("get_album_meta", "get_track_meta"):
                    res = list(res)
                if isinstance(res, dict) and "raw_key" in res:
                    res["raw_key"] = res["raw_key"].hex()
                out["result"] = res
        except SystemExit as e:
            out["raised"] = f"SystemExit({e.code})"
        except Exception as e:
            out["raised"] = type(e).__name__
    out["exchanges"] = exchanges(rec)
    return out


def gen_request_params():
    g = Golden("request_params")
    status = lambda code, body="": (lambda q: (code, body))
    scenarios = [
        ("new client", {}, {}, None),
        ("new client without force_english", {"force_english": False}, {}, None),
        ("legacy app ID fetches the bundle", {"app_id": "798273057", "secrets": ["unused"]}, {}, None),
        ("empty app ID fetches the bundle", {"app_id": ""}, {}, None),
        ("failed bundle keeps the given values", {"app_id": "798273057"}, {"play": lambda path: (500, "down")}, None),
        ("bundle without a script keeps the given values", {"app_id": "798273057"}, {"play": lambda path: (200, "<html></html>")}, None),
        ("user/get 401 exits", {}, {"user/get": status(401, '{"status":"error","code":401,"message":"User authentication is required."}')}, None),
        ("user/get 400 is an empty answer", {}, {"user/get": status(400, '{"status":"error"}')}, None),
        ("user/get 500 is bypassed", {}, {"user/get": status(500, "oops")}, None),
        ("user/get 404 is bypassed", {}, {"user/get": status(404, "{}")}, None),
        ("user/get not JSON is bypassed", {}, {"user/get": status(200, "<html>")}, None),
        ("user/get with the user nested", {}, {"user/get": status(200, '{"user":{"id":7,"credential":{"parameters":{"short_label":"Sublime"}}}}')}, None),
        ("user/get with a null user", {}, {"user/get": status(200, '{"user":null}')}, None),
        ("user/get with a null label", {}, {"user/get": status(200, '{"id":0,"credential":{"parameters":{"short_label":null}}}')}, None),
        ("user/get with a string credential", {}, {"user/get": status(200, '{"id":5,"credential":"x"}')}, None),
        ("user/get with a list answer", {}, {"user/get": status(200, '[1]')}, None),
        ("no token and a short password exits", {"token": "", "pwd": "short"}, {}, None),
        ("a long password is the token", {"token": "", "pwd": "p" * 61}, {}, None),
        ("a 60-character password exits", {"token": None, "pwd": "p" * 60}, {}, None),
        ("token wins over a long password", {"pwd": "p" * 61}, {}, None),
        ("no working secret keeps the first", {"secrets": [BAD_SECRET, "deadbeef"]}, {}, None),
        ("no secrets", {"secrets": []}, {}, None),
        ("an empty first secret that fails", {"secrets": ["", FAKE_SECRET]}, {}, None),
        ("only an empty secret", {"secrets": [""]}, {}, None),
        ("the good secret first", {"secrets": [FAKE_SECRET, BAD_SECRET]}, {}, None),
        ("getFileUrl 500 counts as a failure", {"secrets": [FAKE_SECRET, BAD_SECRET]}, {"track/getFileUrl": status(500, "{}")}, None),
    ]
    calls = [
        ("album/get", ("get_album_meta", "0634904080853")),
        ("track/get", ("get_track_meta", "20273753")),
        ("playlist pages", ("get_plist_meta", "2082798")),
        ("artist pages", ("get_artist_meta", "50782")),
        ("label pages", ("get_label_meta", "572190")),
        ("search albums", ("search_albums", "kind of blue")),
        ("search tracks", ("search_tracks", "so what", 10)),
        ("search artists", ("search_artists", "miles & co/x")),
        ("search playlists", ("search_playlists", "jazz été +1")),
        ("track URL at 5", ("get_track_url", "20273753", 5)),
        ("track URL at 27", ("get_track_url", "20273753", 27)),
        ("track URL at 6", ("get_track_url", 20273753, 6)),
        ("getFileUrl at 4", ("api_call", "track/getFileUrl", {"id": "1", "fmt_id": 4})),
        ("file/url at 27", ("api_call", "file/url", {"id": "20273753", "fmt_id": 27})),
        ("file/url at 5", ("api_call", "file/url", {"id": "20273753", "fmt_id": 5})),
        ("session/start", ("api_call", "session/start", {})),
        ("user/get", ("api_call", "user/get", {})),
    ]
    for name, opts, overrides, call in scenarios:
        g.case(name, {"client": dict(CLIENT_DEFAULTS, **opts)}, lambda o=opts, ov=overrides: run_client(o, ov, None))
    for name, call in calls:
        g.case(f"call: {name}", {"client": CLIENT_DEFAULTS, "call": list(call)}, lambda call=call: run_client({}, {}, call))
    bad = [
        ("getFileUrl 400 at 27", {"track/getFileUrl": lambda q: (400, '{"message":"bad"}') if q["track_id"] == "1" else None},
         ("api_call", "track/getFileUrl", {"id": "1", "fmt_id": 27})),
        ("getFileUrl 403 at 27", {"track/getFileUrl": lambda q: (403, '{"message":"no"}') if q["track_id"] == "1" else None},
         ("api_call", "track/getFileUrl", {"id": "1", "fmt_id": 27})),
        ("track URL at 5 after a 400", {"track/getFileUrl": lambda q: (400, '{"message":"bad"}') if q["track_id"] == "1" else None},
         ("get_track_url", "1", 5)),
        ("album/get 404", {"album/get": status(404, '{"status":"error","code":404,"message":"Album not found"}')},
         ("get_album_meta", "nope")),
        ("search error", {"catalog/search": status(500, "{}")}, ("search_albums", "kind of blue")),
    ]
    for name, overrides, call in bad:
        g.case(f"call: {name}", {"client": CLIENT_DEFAULTS, "call": list(call)},
               lambda ov=overrides, call=call: run_client({}, ov, call))
    for name, pages in multi_meta_scenarios():
        g.case(f"multi_meta: {name}", {"client": CLIENT_DEFAULTS, "call": ["get_plist_meta", "77"]},
               lambda pages=pages: run_client({}, {"playlist/get": pager(pages)}, ("get_plist_meta", "77")))
    g.write()


def pager(pages):
    state = {"i": 0}

    def answer(q):
        i = state["i"]
        state["i"] += 1
        return 200, json.dumps(pages[i])

    return answer


def multi_meta_scenarios():
    def page(off, n, total=None, count=None, extra=None):
        tracks = {"offset": off, "limit": 50, "items": [{"id": off + i} for i in range(n)]}
        if total is not None:
            tracks["total"] = total
        p = {"id": 77, "name": "P", "tracks": tracks}
        if count is not None:
            p["tracks_count"] = count
        if extra:
            p.update(extra)
        return p

    return [
        ("total reached", [page(0, 50, 120), page(50, 50, 120), page(100, 20, 120)]),
        ("empty page stops", [page(0, 50, 200), page(50, 0, 200)]),
        ("first page empty", [page(0, 0, 0)]),
        ("tracks_count without total", [page(0, 50, None, 60), page(50, 10, None, 60)]),
        ("neither total nor count", [page(0, 3)]),
        ("short page below the total", [page(0, 30, 100), page(30, 30, 100), page(60, 40, 100)]),
        ("float total", [page(0, 2, 2.5), page(2, 1, 2.5)]),
        ("null items", [{"id": 77, "tracks": {"items": None, "total": 5}}]),
        ("null tracks", [{"id": 77, "tracks": None}]),
        ("string total", [page(0, 2, "2")]),
    ]


def main():
    logging.disable(logging.CRITICAL)
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
    gen_unicode()
    gen_clean_filename(corpus, recorded, attr_sets)
    gen_pathvalidate(corpus, recorded)
    gen_url_info()
    gen_normalize_json(index, raw)
    gen_name_length(corpus)
    gen_album_artist(index, raw)
    gen_smart_discography(index, raw)
    gen_release_type(index, raw)
    gen_settings()
    write_bundle_fixtures()
    gen_secrets()
    gen_signatures()
    gen_session_key()
    gen_request_params()


if __name__ == "__main__":
    main()
