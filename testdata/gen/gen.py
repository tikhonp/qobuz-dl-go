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
import copy
import hashlib
import io
import json
import os
import logging
import math
import random
import re
import shutil
import sqlite3
import struct
import sys
import tempfile
import textwrap
import threading
import unicodedata
import urllib.parse
from http import HTTPStatus
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from types import SimpleNamespace

import mutagen
import mutagen.id3 as id3
import pathvalidate
import requests
from cryptography.hazmat.primitives import padding
from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes
from mutagen.flac import FLAC
from pathvalidate import sanitize_filename, sanitize_filepath
from requests.adapters import BaseAdapter

from qobuz_dl import bundle as bundle_mod
from qobuz_dl import constants, core, db, downloader, metadata, qopy, utils
from qobuz_dl.core import QobuzDL
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


def client_call(c, call):
    fn, args = call[0], call[1:]
    if fn == "api_call":
        res = c.api_call(args[0], **args[1])
    else:
        res = getattr(c, fn)(*args)
    if fn.startswith("get_") and fn.endswith("_meta") and fn not in ("get_album_meta", "get_track_meta"):
        res = list(res)
    if isinstance(res, dict) and "raw_key" in res:
        res["raw_key"] = res["raw_key"].hex()
    return res


def run_client(opts, overrides, call):
    o = dict(CLIENT_DEFAULTS, **opts)
    out = {}
    # Upstream prints and logs as it goes; the golden needs none of it.
    with offline(api_answer(overrides)) as rec, contextlib.redirect_stdout(io.StringIO()):
        try:
            c = Client(o["email"], o["pwd"], o["app_id"], o["secrets"], o["token"], force_english=o["force_english"])
            out["state"] = client_state(c)
            if call and call[0] == "seq":
                out["result"] = [client_call(c, sub) for sub in call[1:]]
            elif call:
                out["result"] = client_call(c, call)
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
    for name, overrides, call in segmented_calls():
        g.case(f"segmented: {name}", {"client": CLIENT_DEFAULTS, "call": list(call)},
               lambda ov=overrides, call=call: run_client({}, ov, call))
    g.write()


def segmented_calls():
    """Calls through get_track_url's segmented branch, with the getFileUrl,
    session/start and file/url answers each one needs."""
    # cfg_setup's call keeps the default answer, so the client picks the
    # secret the wrapped key needs.
    status = lambda code, body="": (lambda q: None if q.get("track_id") == "5966783" else (code, body))
    no_url = {"track/getFileUrl": lambda q: None if q["track_id"] == "5966783" else (
        200, json.dumps({"track_id": int(q["track_id"]), "format_id": int(q["format_id"]),
                         "restrictions": [{"code": "TrackRestrictedByRightHolders"}]}))}

    def file_url(**change):
        def answer(q):
            a = file_url_answer(q)
            for k, v in change.items():
                if v is MISSING:
                    a.pop(k, None)
                else:
                    a[k] = v
            return 200, json.dumps(a)
        return answer

    def seg(over):
        return dict(no_url, **over)

    yield "track URL at 27 without url", no_url, ("get_track_url", "20273753", 27)
    yield "track URL at 7 forced", {}, ("get_track_url", "20273753", 7, True)
    yield "track URL at 6 after a 400", {"track/getFileUrl": status(400, '{"message":"bad"}')}, ("get_track_url", "20273753", 6)
    yield "track URL at 27 after a 500", {"track/getFileUrl": status(500, "{}")}, ("get_track_url", "20273753", 27)
    yield "track URL at 27 after a list answer", {"track/getFileUrl": status(200, "[1]")}, ("get_track_url", "20273753", 27)
    yield "track URL at 4", {}, ("get_track_url", "20273753", 4)
    yield "track URL at 5 without url", no_url, ("get_track_url", "20273753", 5)
    yield "session reused", {}, ("seq", ["get_track_url", "20273753", 27, True], ["get_track_url", "20273754", 7, True])
    yield "bit_depth kept", seg({"file/url": file_url(bit_depth=16)}), ("get_track_url", "1", 27)
    yield "no bits_depth", seg({"file/url": file_url(bits_depth=MISSING)}), ("get_track_url", "1", 27)
    yield "sampling_rate 44.1", seg({"file/url": file_url(sampling_rate=44.1)}), ("get_track_url", "1", 27)
    yield "sampling_rate 1000", seg({"file/url": file_url(sampling_rate=1000)}), ("get_track_url", "1", 27)
    yield "sampling_rate 1001", seg({"file/url": file_url(sampling_rate=1001)}), ("get_track_url", "1", 27)
    yield "sampling_rate 192000.0", seg({"file/url": file_url(sampling_rate=192000.0)}), ("get_track_url", "1", 27)
    yield "sampling_rate missing", seg({"file/url": file_url(sampling_rate=MISSING)}), ("get_track_url", "1", 27)
    yield "sampling_rate None", seg({"file/url": file_url(sampling_rate=None)}), ("get_track_url", "1", 27)
    yield "sampling_rate a string", seg({"file/url": file_url(sampling_rate="96000")}), ("get_track_url", "1", 27)
    yield "no key", seg({"file/url": file_url(key=MISSING)}), ("get_track_url", "1", 27)
    yield "key not a string", seg({"file/url": file_url(key=5)}), ("get_track_url", "1", 27)
    yield "key in two parts", seg({"file/url": file_url(key="qbz-1.abc")}), ("get_track_url", "1", 27)
    yield "empty answer", seg({"file/url": status(200, "{}")}), ("get_track_url", "1", 27)
    yield "file/url 400", seg({"file/url": status(400, '{"message":"bad"}')}), ("get_track_url", "1", 27)
    yield "file/url 404", seg({"file/url": status(404, "{}")}), ("get_track_url", "1", 27)
    yield "session/start 500", seg({"session/start": status(500, "{}")}), ("get_track_url", "1", 27)
    yield "session/start without infos", seg({"session/start": status(200, '{"session_id":"s"}')}), ("get_track_url", "1", 27)
    yield "session/start with bad infos", seg({"session/start": status(200, '{"session_id":"s","infos":"nodot"}')}), ("get_track_url", "1", 27)


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


# --- Tags -----------------------------------------------------------------

AUDIO = FIXTURES / "audio"
MISSING = object()
# The album keys _get_tags_to_add and get_album_artist read. Cases built from
# the fixtures keep only these, so that each doesn't carry the track list.
TAG_ALBUM_KEYS = ("id", "title", "version", "artist", "artists", "release_date_original", "genre",
                  "genres_list", "copyright", "label", "upc", "product_type", "release_type",
                  "media_count", "tracks_count")
FLAC_LIMIT = metadata.FLAC_MAX_BLOCKSIZE


def tag_settings(over):
    s = QobuzDLSettings()
    for k, v in over.items():
        if not hasattr(s, k):
            raise ValueError(f"no setting {k}")
        setattr(s, k, QobuzDLSettings.parse_aliases(v) if k == "various_artists_aliases" else v)
    return s


def no_tag_settings():
    return sorted(k for k in vars(QobuzDLSettings()) if k.startswith("no_") and k.endswith("_tag"))


def tag_album(album, tracks=False):
    out = {k: album[k] for k in TAG_ALBUM_KEYS if k in album}
    if tracks:
        out["tracks"] = {"items": [{k: t[k] for k in ("media_number",) if k in t}
                                   for t in album["tracks"]["items"]]}
    return out


def overridden(base, over):
    out = copy.deepcopy(base)
    for k, v in (over or {}).items():
        if v is MISSING:
            out.pop(k, None)
        else:
            out[k] = v
    return out


def base_album(**over):
    return overridden({
        "id": "alb1", "title": "Album", "version": None, "artist": {"name": "Main"},
        "release_date_original": "2020-05-17", "genre": {"name": "Jazz"}, "genres_list": ["Jazz"],
        "copyright": "(P) 2020 Label (C) 2020 Label", "label": {"name": "Label"}, "upc": "0123456789012",
        "product_type": "album", "release_type": "album", "tracks_count": 1, "media_count": 1,
    }, over)


def base_item(**over):
    return overridden({
        "id": 1, "title": "Song", "version": None, "performer": {"name": "Main"},
        "performers": "Main, MainArtist", "isrc": "XX0000000001", "track_number": 1, "media_number": 1,
        "parental_warning": False,
        "audio_info": {"replaygain_track_gain": -6.5, "replaygain_track_peak": 0.98},
    }, over)


def tag_fixture_inputs(index, raw):
    items = index["items"]
    for kind, album_id in items["albums"].items():
        album = normalize(raw[f"album-get-{album_id}.json"])
        for t in album["tracks"]["items"]:
            yield f"fixture/album/{kind}/{t['id']}", tag_album(album), t
    album = normalize(raw[f"album-get-{items['albums']['classical']}.json"])
    yield "fixture/whole classical album", album, album["tracks"]["items"][0]
    track = normalize(raw[f"track-get-{items['track']}.json"])
    yield f"fixture/track/{items['track']}", track["album"], track
    for t in normalize(raw[f"playlist-get-{items['playlist']}-0.json"])["tracks"]["items"]:
        yield f"fixture/playlist/{t['id']}", t["album"], t


def built_tag_inputs():
    A, I = base_album, base_item
    pf = lambda *blocks: " - ".join(blocks)
    yield "basic", A(), I(), {}
    yield "empty album", {}, I(), {}
    yield "album None", None, I(), {}
    yield "empty item", A(), {}, {}

    # Titles and versions
    yield "title/version appended", A(version="Deluxe"), I(version="Live"), {}
    yield "title/version already in title", A(title="Album (DELUXE Edition)", version="deluxe"), I(title="Song - live", version="Live"), {}
    yield "title/version empty", A(version=""), I(version=""), {}
    yield "title/title missing", A(title=MISSING), I(title=MISSING), {}
    yield "title/title None without version", A(title=None), I(title=None), {}
    yield "title/title None with version", A(title=None, version="Live"), I(), {}
    yield "title/version a number", A(version=2), I(), {}
    yield "title/title a number", A(title=5), I(title=7), {}
    yield "title/title a number with version", A(), I(title=7, version="Live"), {}
    yield "title/casefold is not lower", A(title="STRASSE", version="straße"), I(title="İ", version="i̇"), {}

    # ARTIST
    yield "artist/featured", A(), I(performers=pf("Main, MainArtist", "Guest, FeaturedArtist", "Prod, Producer")), {}
    yield "artist/main repeated in another case", A(), I(performers=pf("MAIN, MainArtist", "main, FeaturedArtist")), {}
    yield "artist/combined credit dropped", A(), I(performer={"name": "A & B"}, performers=pf("A, MainArtist", "B, MainArtist")), {}
    yield "artist/combined credit with comma", A(), I(performer={"name": "A, B & C"}, performers=pf("a, MainArtist", "B, MainArtist", "c, FeaturedArtist")), {}
    yield "artist/combined credit partly listed", A(), I(performer={"name": "A & B"}, performers=pf("A, MainArtist", "C, MainArtist")), {}
    yield "artist/band name with comma", A(), I(performer={"name": "Earth, Wind & Fire"}, performers=pf("Earth, Wind & Fire, MainArtist", "Guest, FeaturedArtist")), {}
    yield "artist/band name with ampersand", A(), I(performer={"name": "Mumford & Sons"}, performers=pf("Mumford & Sons, MainArtist")), {}
    yield "artist/band name parts listed", A(), I(performer={"name": "Mumford & Sons"}, performers=pf("Mumford, MainArtist", "Sons, MainArtist")), {}
    yield "artist/only roles that aren't artists", A(), I(performers=pf("Writer, Composer", "Main, Producer")), {}
    yield "artist/name only blocks ignored", A(), I(performers=pf("Lonely", "Guest, FeaturedArtist")), {}
    yield "artist/empty name", A(), I(performers=pf(", MainArtist", "Guest, FeaturedArtist")), {}
    yield "artist/spaces around parts", A(), I(performers=" Guest ,  FeaturedArtist  - Other , MainArtist"), {}
    yield "artist/casefold dedupe", A(), I(performer={"name": "STRASSE"}, performers=pf("straße, MainArtist")), {}
    yield "artist/album artist fallback", A(), I(performer=MISSING, performers=MISSING), {}
    yield "artist/performer name empty", A(artist={"name": "Album Artist"}), I(performer={"name": ""}), {}
    yield "artist/nobody", A(artist={}), I(performer={}, performers=""), {}
    yield "artist/nobody but performers", A(artist={}), I(performer={}, performers=pf("X, MainArtist", "Y, FeaturedArtist")), {}
    yield "artist/main artist a number", A(), I(performer={"name": 5}, performers=MISSING), {}
    yield "artist/main artist a number with performers", A(), I(performer={"name": 5}), {}
    yield "artist/performer None", A(), I(performer=None), {}
    yield "artist/album artist None", A(artist=None), I(performer={}), {}
    yield "artist/performers a list", A(), I(performers=["Main, MainArtist"]), {}
    yield "artist/performers None", A(), I(performers=None), {}
    yield "artist/main with only one part", A(), I(performer={"name": "Solo,"}, performers=pf("Solo, MainArtist", "Guest, FeaturedArtist")), {}

    # COMPOSER
    yield "composer/from performers", A(), I(performers=pf("Main, MainArtist", "Writer, Composer", "Lyr, ComposerLyricist, Lyricist")), {}
    yield "composer/repeated in another case", A(), I(performers=pf("Writer, Composer", "WRITER, ComposerLyricist")), {}
    yield "composer/fallback", A(), I(composer={"name": "Fallback"}), {}
    yield "composer/performers win over fallback", A(), I(composer={"name": "Fallback"}, performers=pf("Writer, Composer")), {}
    yield "composer/none", A(), I(composer={}), {}
    yield "composer/name a number", A(), I(composer={"name": 5}), {}
    yield "composer/composer None", A(), I(composer=None), {}
    yield "composer/lyricist only", A(), I(performers=pf("Lyr, Lyricist")), {}

    # DATE
    yield "date/missing", A(release_date_original=MISSING), I(), {}
    yield "date/None", A(release_date_original=None), I(), {}

    # GENRE
    yield "genre/main replaces first", A(genre={"name": "Rock"}, genres_list=["Pop/Rock", "Pop/Rock→Rock→Hard Rock"]), I(), {}
    yield "genre/french main", A(genre={"name": "Classique"}, genres_list=["Classique", "Classique→Opéra"]), I(), {}
    yield "genre/french list", A(genre={"name": "Jazz"}, genres_list=["Jazz", "Jazz→Jazz Vocal", "Musiques du monde"]), I(), {}
    yield "genre/repeats dropped", A(genre={"name": "Rock"}, genres_list=["Rock", "Rock", "Rock→Rock"]), I(), {}
    yield "genre/list empty", A(genre={"name": "Électronique"}, genres_list=[]), I(), {}
    yield "genre/list missing", A(genres_list=MISSING), I(), {}
    yield "genre/no main", A(genre={}, genres_list=["Classique→Opéra"]), I(), {}
    yield "genre/genre missing", A(genre=MISSING, genres_list=["Pop"]), I(), {}
    yield "genre/nothing", A(genre={}, genres_list=[]), I(), {}
    yield "genre/blank pieces", A(genre={"name": "Jazz"}, genres_list=["Jazz", " ", "→→", " Blues "]), I(), {}
    yield "genre/slash kept", A(genre={"name": "Pop/Rock"}, genres_list=["x"]), I(), {}
    yield "genre/list None without main", A(genre={}, genres_list=None), I(), {}
    yield "genre/list None with main", A(genres_list=None), I(), {}
    yield "genre/list a string without main", A(genre={}, genres_list="Pop"), I(), {}
    yield "genre/list a string with main", A(genres_list="Pop"), I(), {}
    yield "genre/list with a number", A(genre={}, genres_list=["Pop", 5]), I(), {}
    yield "genre/main a number", A(genre={"name": 5}), I(), {}
    yield "genre/main a list", A(genre={"name": ["Jazz"]}), I(), {}
    yield "genre/main empty", A(genre={"name": ""}, genres_list=["Pop"]), I(), {}
    yield "genre/genre None", A(genre=None), I(), {}
    yield "genre/list a dict without main", A(genre={}, genres_list={"Pop": 1, "Rock": 2}), I(), {}
    yield "genre/list a dict with main", A(genres_list={"Pop": 1}), I(), {}

    # COPYRIGHT
    yield "copyright/missing", A(copyright=MISSING), I(), {}
    yield "copyright/null", A(copyright=None), I(), {}
    yield "copyright/empty", A(copyright=""), I(), {}
    yield "copyright/symbols", A(copyright="(P)(C) (p) (c) ((P))"), I(), {}
    yield "copyright/a number", A(copyright=2020), I(), {}
    yield "copyright/zero", A(copyright=0), I(), {}

    # LABEL
    yield "label/whitespace runs", A(label={"name": " Big\t\tLabel  Records\n"}), I(), {}
    yield "label/missing", A(label=MISSING), I(), {}
    yield "label/name missing", A(label={}), I(), {}
    yield "label/None", A(label=None), I(), {}
    yield "label/name None", A(label={"name": None}), I(), {}

    # ISRC, BARCODE, MEDIATYPE, ITUNESADVISORY
    yield "values/isrc and upc missing", A(upc=MISSING), I(isrc=MISSING), {}
    yield "values/isrc and upc None", A(upc=None), I(isrc=None), {}
    yield "values/isrc and upc numbers", A(upc=123), I(isrc=5), {}
    yield "values/media type cases", A(product_type="straße épée"), I(), {}
    yield "values/media type missing", A(product_type=MISSING), I(), {}
    yield "values/media type None", A(product_type=None), I(), {}
    yield "values/explicit", A(), I(parental_warning=True), {}
    yield "values/explicit truthy", A(), I(parental_warning="yes"), {}
    yield "values/explicit missing", A(), I(parental_warning=MISSING), {}

    # COMPILATION and RELEASETYPE
    yield "compilation/various artists", A(artist={"name": "Various Artists"}, release_type="album"), I(), {}
    yield "compilation/alias with spaces and case", A(artist={"name": "  VA "}, release_type=None, product_type="album"), I(), {}
    yield "compilation/main artists list", A(artists=[{"name": "Varios Artistas", "roles": ["main-artist"]}]), I(), {}
    yield "compilation/release type compilation", A(release_type="compilation"), I(), {}
    yield "compilation/custom aliases", A(artist={"name": "Main"}), I(), {"various_artists_aliases": "main, other"}
    yield "compilation/aliases empty", A(artist={"name": "Various Artists"}), I(), {"various_artists_aliases": ""}
    yield "compilation/epmini", A(release_type="epmini", product_type="ep"), I(), {}
    yield "compilation/no type", A(release_type=None, product_type=""), I(), {}
    yield "compilation/no type various", A(release_type=None, product_type="", artist={"name": "Various"}), I(), {}

    # ReplayGain
    for name, gain, peak in [
        ("negative", -6.5, 0.98), ("ints", -7, 1), ("zero", 0, 0.0), ("negative zero", -0.0, -0.0),
        ("small", 1e-05, 0.0001), ("large", 1e16, 123456789012345678), ("many digits", -10.123456789012345, 0.1 + 0.2),
        ("strings", "-6.5", "0.98"), ("bools", True, False), ("None", None, None),
    ]:
        yield f"replaygain/{name}", A(), I(audio_info={"replaygain_track_gain": gain, "replaygain_track_peak": peak}), {}
    yield "replaygain/missing keys", A(), I(audio_info={"other": 1}), {}
    yield "replaygain/audio_info missing", A(), I(audio_info=MISSING), {}
    yield "replaygain/audio_info None", A(), I(audio_info=None), {}
    yield "replaygain/audio_info empty list", A(), I(audio_info=[]), {}
    yield "replaygain/audio_info a string", A(), I(audio_info="x"), {}
    yield "replaygain/off", A(), I(), {"no_replaygain_tag": True}

    # Classical
    yield "classical/work conductor ensemble", A(), I(work="Symphony No. 9", performers=pf(
        "Ludwig van Beethoven, Composer", "Herbert von Karajan, Conductor, MainArtist",
        "Berliner Philharmoniker, Orchestra, MainArtist", "Wiener Singverein, Choir", "Ensemble X, Ensemble")), {}
    yield "classical/two conductors", A(), I(performers=pf("C1, Conductor", "C2, Conductor", "C1, Conductor")), {}
    yield "classical/work a number", A(), I(work=9), {}
    yield "classical/work empty", A(), I(work=""), {}
    yield "classical/everything off", A(), I(work="W", performers=pf("C, Conductor", "O, Orchestra")), {
        "no_work_tag": True, "no_conductor_tag": True, "no_ensemble_tag": True}

    # IDs and the album URL
    yield "ids/zero and empty", A(id=""), I(id=0), {}
    yield "ids/None", A(id=None), I(id=None), {}
    yield "ids/numbers", A(id=12345), I(id=1.5), {}
    for title in ["Mahler: Symphony No. 2", "...'Til We Meet Again (Live)", "Ümlaut & Co", "---", "İstanbul", "", "ABC 123 xyz"]:
        yield f"url/title {title!r}", A(title=title), I(), {}
    yield "url/title missing", A(title=MISSING), I(), {}
    yield "url/title None", A(title=None), I(), {}
    yield "url/title a number", A(title=42), I(), {}
    yield "url/off", A(), I(), {"no_album_url_tag": True}


def gen_tags(index, raw):
    g = Golden("tags")

    def run(album, item, over):
        return metadata._get_tags_to_add(copy.deepcopy(album), copy.deepcopy(item), settings=tag_settings(over))

    def case(name, album, item, over):
        g.case(name, {"album": album, "item": item, "settings": over}, lambda: run(album, item, over))

    for name, album, item in tag_fixture_inputs(index, raw):
        case(name, album, item, {})
    for name, album, item, over in built_tag_inputs():
        case(name, album, item, over)

    album = tag_album(normalize(raw[f"album-get-{index['items']['albums']['classical']}.json"]))
    item = normalize(raw[f"album-get-{index['items']['albums']['classical']}.json"])["tracks"]["items"][0]
    comp = normalize(raw[f"album-get-{index['items']['albums']['compilation']}.json"])
    for key in no_tag_settings():
        case(f"settings/{key}/classical", album, item, {key: True})
        case(f"settings/{key}/compilation", tag_album(comp), comp["tracks"]["items"][0], {key: True})
    case("settings/all off", album, item, {k: True for k in no_tag_settings()})
    g.write()


def gen_metadata_helpers():
    g = Golden("metadata_helpers")
    for title, version in [("T", ""), ("T", None), ("T", "Live"), ("T (Live)", "live"), ("", "Live"),
                           (None, None), (None, "Live"), (5, None), ("T", 0), ("T", 5), ("ǅ", "ǆ"), ("STRASSE", "straße")]:
        g.case(f"title_with_version {title!r} {version!r}", {"fn": "title_with_version", "title": title, "version": version},
               lambda t=title, v=version: metadata._get_title_with_version(t, v))
    for d in [{"title": "T"}, {"title": "T", "version": "Live"}, {"title": "T (Live)", "version": "Live"},
              {"title": "T", "work": "W"}, {"title": "T", "version": "V", "work": "W"}, {"title": None},
              {"title": 5, "version": 1.5, "work": True}, {"version": "V"}, {"title": "T", "version": None, "work": ""}]:
        g.case(f"get_title {d!r}", {"fn": "get_title", "track": d}, lambda d=d: metadata._get_title(d))
    for s in ["", None, "(P) 2020 (C) 2021", "((P))", "(p)", 0, 5]:
        g.case(f"format_copyright {s!r}", {"fn": "format_copyright", "s": s}, lambda s=s: metadata._format_copyright(s))
    for genres in [[], ["Pop"], ["Pop/Rock", "Rock"], ["Pop→Rock", "Rock/Pop"], ["a//b", "→", "a"], [" x ", "x"], ["Pop", 5]]:
        g.case(f"format_genres {genres!r}", {"fn": "format_genres", "genres": genres},
               lambda gs=genres: metadata._format_genres(gs))
    g.write()


def gen_release_types(index, raw):
    g = Golden("release_types")
    default = sorted(QobuzDLSettings().various_artists_aliases)

    def case(name, album, aliases):
        g.case(name, {"album": album, "aliases": aliases},
               lambda: {"types": metadata._release_types(copy.deepcopy(album), set(aliases)),
                        "various": metadata._is_various_artists(copy.deepcopy(album), set(aliases))})

    for name, album in album_dicts(index, raw):
        case(name, tag_album(album), default)
    raws = [MISSING, None, "", "album", "Album", "ALBUM", "single", "ep", "EP", "compilation", "Compilation",
            "epmini", "live", 0, 5, True, ["album"]]
    for rt in raws:
        for pt in (MISSING, "album", "ep", "compilation"):
            for artist in ("Main", "Various Artists"):
                album = overridden({"artist": {"name": artist}}, {"release_type": rt, "product_type": pt})
                case(f"release_type {'missing' if rt is MISSING else repr(rt)} product_type "
                     f"{'missing' if pt is MISSING else repr(pt)} artist {artist!r}", album, default)
    case("artist None", {"artist": None, "release_type": "album"}, default)
    case("artists an error", {"artists": 5, "artist": {"name": "VA"}, "release_type": "single"}, default)
    case("artist name a number", {"artist": {"name": 5}, "release_type": "single"}, ["5"])
    case("custom aliases", {"artist": {"name": "Main"}, "release_type": "single"}, ["main"])
    case("no aliases", {"artist": {"name": "Various Artists"}, "release_type": "single"}, [])
    case("several main artists one alias", {"artists": [{"name": "A", "roles": ["main-artist"]},
                                                        {"name": " VA ", "roles": ["main-artist"]}]}, default)
    g.write()


def gen_disc_track_total(index, raw):
    g = Golden("disc_track_total")

    def case(name, album, item):
        g.case(name, {"album": album, "item": item},
               lambda: metadata._disc_track_total(copy.deepcopy(album), copy.deepcopy(item)))

    items = index["items"]
    for kind, album_id in items["albums"].items():
        album = normalize(raw[f"album-get-{album_id}.json"])
        trimmed = {k: album[k] for k in ("tracks_count", "media_count") if k in album}
        trimmed["tracks"] = tag_album(album, tracks=True)["tracks"]
        for t in album["tracks"]["items"]:
            case(f"album/{kind}/{t['id']}", trimmed, {k: t[k] for k in ("media_number",) if k in t})
    track = normalize(raw[f"track-get-{items['track']}.json"])
    case(f"track/{items['track']}", track["album"], track)
    for t in normalize(raw[f"playlist-get-{items['playlist']}-0.json"])["tracks"]["items"]:
        case(f"playlist/{t['id']}", t["album"], t)

    disc = lambda *ns: {"items": [{"media_number": n} if n is not MISSING else {} for n in ns]}
    built = [
        ("two discs complete", {"tracks_count": 5, "media_count": 2, "tracks": disc(1, 1, 1, 2, 2)}, {"media_number": 2}),
        ("two discs complete first", {"tracks_count": 5, "media_count": 2, "tracks": disc(1, 1, 1, 2, 2)}, {"media_number": 1}),
        ("two discs incomplete", {"tracks_count": 6, "media_count": 2, "tracks": disc(1, 1, 1, 2, 2)}, {"media_number": 2}),
        ("one disc", {"tracks_count": 5, "media_count": 1, "tracks": disc(1, 1, 1, 2, 2)}, {"media_number": 2}),
        ("disc not in list", {"tracks_count": 2, "media_count": 2, "tracks": disc(1, 1)}, {"media_number": 3}),
        ("media_number missing", {"tracks_count": 3, "media_count": 2, "tracks": disc(MISSING, 1, 2)}, {}),
        ("media_number a string", {"tracks_count": 2, "media_count": 2, "tracks": disc(1, 2)}, {"media_number": "1"}),
        ("media_number a float", {"tracks_count": 2, "media_count": 2, "tracks": disc(1, 2)}, {"media_number": 1.0}),
        ("media_number None", {"tracks_count": 2, "media_count": 2, "tracks": disc(None, 2)}, {"media_number": None}),
        ("nothing", {}, {}),
        ("tracks_count zero", {"tracks_count": 0}, {}),
        ("tracks_count None", {"tracks_count": None}, {}),
        ("tracks_count a string", {"tracks_count": "5", "media_count": 2, "tracks": disc(1, 1, 1, 2, 2)}, {"media_number": 2}),
        ("tracks_count a float", {"tracks_count": 5.0, "media_count": 2, "tracks": disc(1, 1, 1, 2, 2)}, {"media_number": 2}),
        ("media_count a string", {"tracks_count": 2, "media_count": "2", "tracks": disc(1, 2)}, {"media_number": 1}),
        ("media_count a float", {"tracks_count": 2, "media_count": 1.5, "tracks": disc(1, 2)}, {"media_number": 1}),
        ("media_count None", {"tracks_count": 2, "media_count": None, "tracks": disc(1, 2)}, {"media_number": 1}),
        ("media_count True", {"tracks_count": 2, "media_count": True, "tracks": disc(1, 2)}, {"media_number": 1}),
        ("tracks None", {"tracks_count": 2, "media_count": 2, "tracks": None}, {"media_number": 1}),
        ("tracks a string", {"tracks_count": 2, "media_count": 2, "tracks": "ab"}, {"media_number": 1}),
        ("items None", {"tracks_count": 2, "media_count": 2, "tracks": {"items": None}}, {"media_number": 1}),
        ("items a string", {"tracks_count": 2, "media_count": 2, "tracks": {"items": "ab"}}, {"media_number": 1}),
        ("items a dict", {"tracks_count": 2, "media_count": 2, "tracks": {"items": {"a": 1, "b": 2}}}, {"media_number": 1}),
        ("items a number", {"tracks_count": 2, "media_count": 2, "tracks": {"items": 5}}, {"media_number": 1}),
        ("item not a dict", {"tracks_count": 2, "media_count": 2, "tracks": {"items": [{"media_number": 1}, 5]}}, {"media_number": 1}),
        ("album None", None, {"media_number": 1}),
    ]
    for name, album, item in built:
        case(f"built/{name}", album, item)
    g.write()


# Audio fixtures made from the ffmpeg ones, so the taggers meet block layouts
# and frames that ffmpeg doesn't write.

def syncsafe(n):
    return bytes([(n >> 21) & 0x7F, (n >> 14) & 0x7F, (n >> 7) & 0x7F, n & 0x7F])


def unsyncsafe(b):
    return (b[0] & 0x7F) << 21 | (b[1] & 0x7F) << 14 | (b[2] & 0x7F) << 7 | (b[3] & 0x7F)


def flac_offset(data):
    if data[:3] == b"ID3":
        return 10 + unsyncsafe(data[6:10])
    return 0


def flac_parts(data):
    """The bytes before fLaC, the metadata blocks and the audio."""
    start = flac_offset(data)
    if data[start:start + 4] != b"fLaC":
        raise ValueError("not a FLAC file")
    pos, blocks = start + 4, []
    while True:
        h = data[pos]
        size = int.from_bytes(data[pos + 1:pos + 4], "big")
        blocks.append((h & 0x7F, data[pos + 4:pos + 4 + size]))
        pos += 4 + size
        if h & 0x80:
            return data[:start], blocks, data[pos:]


def flac_bytes(blocks, audio, prefix=b""):
    out = bytearray(prefix + b"fLaC")
    for i, (code, body) in enumerate(blocks):
        out.append(code | (0x80 if i == len(blocks) - 1 else 0))
        out += len(body).to_bytes(3, "big") + body
    return bytes(out) + audio


def vorbis_block(vendor, comments):
    out = struct.pack("<I", len(vendor)) + vendor + struct.pack("<I", len(comments))
    for c in comments:
        out += struct.pack("<I", len(c)) + c
    return out


def picture_block(ptype, mime, desc, data):
    return (struct.pack(">2I", ptype, len(mime)) + mime + struct.pack(">I", len(desc)) + desc
            + struct.pack(">5I", 0, 0, 0, 0, len(data)) + data)


def write_tag_fixtures():
    _, blocks, audio = flac_parts((AUDIO / "16-44.1.flac").read_bytes())
    streaminfo = blocks[0][1]
    cover = (AUDIO / "cover.jpg").read_bytes()
    comments = [b"TITLE=Old", b"encoder=Lavf", b"Encoded_By=someone", b"ENCODED-BY=x", b"title=Old again",
                b"NOEQUALS", b"=empty key", b"K~EY=tilde", "KÉY=not ascii".encode(), b"BAD=\xff\xfeok\xe2\x82",
                b"GENRE=Old Genre", b"Comment=kept"]
    # One seek point and the start of another, which mutagen drops.
    seektable = struct.pack(">QQH", 0, 0, 4096) + b"\x01\x02\x03"
    (AUDIO / "blocks.flac").write_bytes(flac_bytes([
        (0, streaminfo),
        (1, bytes(16)),
        (2, b"qdlgapplication data"),
        (4, vorbis_block(b"reference libFLAC 1.4.3", comments)),
        (3, seektable),
        (6, picture_block(4, b"image/jpeg", b"back", cover)),
        (4, vorbis_block(b"second", [b"SECOND=1"])),
        (1, bytes(4)),
    ], audio))
    (AUDIO / "no-comments.flac").write_bytes(flac_bytes([(0, streaminfo), (1, bytes(8))], audio))
    tit2 = b"TIT2" + syncsafe(5) + b"\x00\x00" + b"\x00Old\x00"
    prefix = b"ID3\x04\x00\x00" + syncsafe(len(tit2) + 6) + tit2 + bytes(6)
    _, blocks, audio = flac_parts((AUDIO / "16-44.1-tagged.flac").read_bytes())
    (AUDIO / "id3-prefixed.flac").write_bytes(flac_bytes(blocks, audio, prefix))

    mp3 = (AUDIO / "320.mp3").read_bytes()
    bare = mp3[10 + unsyncsafe(mp3[6:10]):]
    (AUDIO / "320-bare.mp3").write_bytes(bare)
    path = AUDIO / "320-tagged.mp3"
    path.write_bytes(bare)
    t = id3.ID3()
    t.add(id3.TPE1(encoding=1, text="Old Artist"))
    t.add(id3.TOPE(encoding=0, text="Original Artist"))
    t.add(id3.TENC(encoding=1, text="Encoder"))
    t.add(id3.TSSE(encoding=0, text="Lavf"))
    t.add(id3.TXXX(encoding=1, desc="QOBUZTRACKID", text="stale"))
    t.add(id3.TXXX(encoding=1, desc="Other", text="kept"))
    t.add(id3.TXXX(encoding=0, desc="Latin", text="kept too"))
    t.add(id3.COMM(encoding=1, lang="eng", desc="c", text="a comment"))
    t.add(id3.APIC(encoding=1, mime="image/jpeg", type=3, desc="", data=b"old cover"))
    t.add(id3.APIC(encoding=0, mime="image/jpeg", type=4, desc="back", data=cover))
    t.save(str(path), v2_version=3)


def place_cover(root, cover):
    """Puts embed_cover.jpg where a case wants it: in the track's folder, its parent, or both."""
    data = (AUDIO / "cover.jpg").read_bytes()
    if cover in ("folder", "both"):
        (root / metadata.EMB_COVER_NAME).write_bytes(data)
    if cover in ("parent", "both"):
        (root.parent / metadata.EMB_COVER_NAME).write_bytes(data[::-1] if cover == "both" else data)
    # Not zeros: mutagen reads a v2.3 APIC whose data is all zeros as empty.
    if cover == "large":
        (root / metadata.EMB_COVER_NAME).write_bytes(b"\xff" * (FLAC_LIMIT + 1))
    if cover == "limit":
        (root / metadata.EMB_COVER_NAME).write_bytes(b"\xff" * FLAC_LIMIT)


def run_tagger(tagger, inp, read):
    with tempfile.TemporaryDirectory() as tmp:
        root = Path(tmp) / "parent" / "album"
        root.mkdir(parents=True)
        place_cover(root, inp["cover"])
        filename, final = root / "~tmp_01.tmp", root / "01 - final"
        shutil.copyfile(AUDIO / inp["file"], filename)
        tagger(str(filename), str(root), str(final), copy.deepcopy(inp["d"]), copy.deepcopy(inp["album"]),
               inp["istrack"], inp["em_image"], settings=tag_settings(inp["settings"]))
        if filename.exists() or not final.exists():
            raise RuntimeError("the tagger didn't rename the file")
        return read(final)


def sha(b):
    return hashlib.sha256(b).hexdigest()


def flac_result(path):
    data = path.read_bytes()
    prefix, blocks, audio = flac_parts(data)
    f = FLAC(str(path))
    return {
        "prefix": sha(prefix),
        "blocks": [[code, len(body)] for code, body in blocks],
        "vendor": f.tags.vendor if f.tags is not None else None,
        "comments": [[k, v] for k, v in f.tags] if f.tags is not None else [],
        "pictures": [[p.type, p.mime, p.desc, sha(p.data)] for p in f.pictures],
        "audio": sha(audio),
        "sha256": sha(data),
    }


def mp3_frame(f):
    out = {"key": f.HashKey, "encoding": int(f.encoding)}
    if isinstance(f, id3.APIC):
        out.update(mime=f.mime, type=int(f.type), data=sha(f.data))
    else:
        out["text"] = [str(x) for x in f.text]
    return out


def mp3_result(path):
    data = path.read_bytes()
    if data[:3] != b"ID3":
        raise ValueError("no ID3 tag")
    end = 10 + unsyncsafe(data[6:10])
    raws, pos = [], 10
    while pos + 10 <= end and data[pos:pos + 4] != bytes(4):
        size = int.from_bytes(data[pos + 4:pos + 8], "big")
        raws.append(sha(data[pos:pos + 10 + size]))
        pos += 10 + size
    tags = id3.ID3(str(path), translate=False)
    frames = sorted((mp3_frame(f) for f in tags.values()), key=lambda f: json.dumps(f, ensure_ascii=False))
    return {"version": data[3], "frames": frames, "raw": sorted(raws), "audio": sha(data[end:])}


def tagger_inputs(index, raw, mp3):
    items = index["items"]
    albums = {k: normalize(raw[f"album-get-{v}.json"]) for k, v in items["albums"].items()}
    track = normalize(raw[f"track-get-{items['track']}.json"])
    playlist = normalize(raw[f"playlist-get-{items['playlist']}-0.json"])["tracks"]["items"]
    files = ["320.mp3", "320-tagged.mp3", "320-bare.mp3"] if mp3 else \
        ["16-44.1.flac", "24-96.flac", "24-192.flac", "16-44.1-tagged.flac"]

    def inp(file, d, album=None, istrack=False, em_image=False, cover="none", settings=None):
        return {"file": file, "d": d, "album": album, "istrack": istrack, "em_image": em_image,
                "cover": cover, "settings": settings or {}}

    def album_track(kind, n=0):
        a = albums[kind]
        return a["tracks"]["items"][n], tag_album(a, tracks=True)

    for i, kind in enumerate(albums):
        for n in sorted({0, len(albums[kind]["tracks"]["items"]) - 1}):
            d, a = album_track(kind, n)
            yield f"album/{kind}/{d['id']}", inp(files[(i + n) % len(files)], d, a)
    yield f"track/{items['track']}", inp(files[0], track, track, istrack=True)
    for t in playlist[:3]:
        yield f"playlist/{t['id']}", inp(files[1], t, t, istrack=True)

    d, a = album_track("classical")
    for cover in ("folder", "parent", "both", "none", "large") + (() if mp3 else ("limit",)):
        yield f"cover/{cover}", inp(files[1], d, a, em_image=True, cover=cover)
    yield "cover/em_image off", inp(files[1], d, a, cover="folder")
    for key in ("no_track_number_tag", "no_track_total_tag", "no_disc_number_tag", "no_disc_total_tag"):
        yield f"numbers/{key}", inp(files[0], d, a, settings={key: True})
    yield "numbers/number and total off", inp(files[0], d, a, settings={"no_track_number_tag": True, "no_disc_number_tag": True})
    yield "settings/all off", inp(files[1], d, a, settings={k: True for k in no_tag_settings()})

    A, I = base_album, base_item
    multi = A(genre={"name": "Rock"}, genres_list=["Pop/Rock", "Pop/Rock→Rock", "Jazz"], copyright="(P) A, B",
              label={"name": "One, Two"})
    multi_item = I(performer={"name": "Earth, Wind & Fire"}, performers="Earth, Wind & Fire, MainArtist")
    for on in (True, False):
        yield f"multi_value_tags/{on}", inp(files[1], multi_item, multi, settings={"multi_value_tags": on})
    yield "multi_value_tags/one genre", inp(files[1], I(), A(), settings={"multi_value_tags": True})
    yield "values/artists list", inp(files[0], I(performers="Main, MainArtist - Guest, FeaturedArtist"), A())
    yield "values/nul in title", inp(files[0], I(title="A\x00B"), A())
    yield "values/isrc a number", inp(files[0], I(isrc=5), A())
    yield "values/work a number", inp(files[0], I(work=5), A())
    yield "values/album artist numbers", inp(files[0], I(), A(artists=[{"name": 7, "roles": ["main-artist"]}]))
    yield "values/album artist empty", inp(files[0], I(), A(artists=[{"name": "", "roles": ["main-artist"]}]))
    yield "values/ensembles list", inp(files[0], I(performers="O1, Orchestra - O2, Choir"), A())
    yield "values/empty strings skipped", inp(files[0], I(isrc="", parental_warning=False), A(upc="", copyright=""))
    yield "values/album None", inp(files[0], I(album=None), None, istrack=True)
    yield "values/album None, numbers off", inp(files[0], I(album=None), None, istrack=True, settings={
        "no_track_number_tag": True, "no_track_total_tag": True, "no_disc_number_tag": True, "no_disc_total_tag": True})
    yield "values/track without album", inp(files[0], I(), None, istrack=True)
    if mp3:
        for name, date in [("year only", "2020"), ("full", "2020-05-17"), ("short parts", "2020-5-17"),
                           ("letters", "abcd-ef-gh"), ("superscript", "²⁰²⁰-⁰⁵-¹⁷"), ("empty", ""),
                           ("None", None), ("a number", 2020), ("a list", ["2020"])]:
            yield f"date/{name}", inp(files[0], I(), A(release_date_original=date))
        yield "date/off", inp(files[0], I(), A(), settings={"no_release_date_tag": True})
        yield "existing/all", inp("320-tagged.mp3", d, a, em_image=True, cover="folder")
        yield "existing/no cover", inp("320-tagged.mp3", I(), A())
        yield "file/flac", inp("16-44.1.flac", I(), A())
    else:
        yield "blocks/all", inp("blocks.flac", d, a, em_image=True, cover="folder")
        yield "blocks/tags off", inp("blocks.flac", I(album={}), None, istrack=True, settings={k: True for k in no_tag_settings()})
        yield "blocks/no comments, nothing to set", inp("no-comments.flac", I(album={}), None, istrack=True,
                                                         settings={k: True for k in no_tag_settings()})
        yield "blocks/no comments", inp("no-comments.flac", d, a, em_image=True, cover="folder")
        yield "blocks/id3 prefix", inp("id3-prefixed.flac", d, a)
        yield "file/mp3", inp("320.mp3", I(), A())


def gen_flac_tagged(index, raw):
    g = Golden("flac_tagged")
    for name, inp in tagger_inputs(index, raw, mp3=False):
        g.case(name, inp, lambda inp=inp: run_tagger(metadata.tag_flac, inp, flac_result))
    g.write()


def gen_mp3_tagged(index, raw):
    g = Golden("mp3_tagged")
    for name, inp in tagger_inputs(index, raw, mp3=True):
        g.case(name, inp, lambda inp=inp: run_tagger(metadata.tag_mp3, inp, mp3_result))
    g.write()


# --- Downloads ------------------------------------------------------------

# The templates the bot runs with, and Download's defaults.
BOT_FOLDER = "{artist} - {album} ({year}) [{bit_depth}B-{sampling_rate}kHz]"
BOT_TRACK = "{track_number} - {track_title}"
NAMING_ROOT = "/ROOT"


def dl_settings(over):
    s = QobuzDLSettings()
    for k, v in over.items():
        if not hasattr(s, k):
            raise KeyError(f"no setting {k}")
        setattr(s, k, v)
    return s


@contextlib.contextmanager
def quiet_downloader():
    """Keeps the downloader's prints, which go to stdout, out of gen.py's output."""
    real = downloader.safe_print
    downloader.safe_print = lambda *a, **k: None
    try:
        yield
    finally:
        downloader.safe_print = real


@contextlib.contextmanager
def recording_fit_name(out):
    """Records the paths _download_and_tag builds, through the _fit_name call that ends them."""
    real = downloader._fit_name

    def fit(parent, name, suffix="", reserve=0):
        res = real(parent, name, suffix, reserve)
        out.append(os.path.join(parent, res))
        return res

    downloader._fit_name = fit
    downloader.abort_event.set()
    try:
        yield
    finally:
        downloader._fit_name = real
        downloader.abort_event.clear()


def relative(path):
    if path == NAMING_ROOT:
        return ""
    if not path.startswith(NAMING_ROOT + "/"):
        raise ValueError(f"{path!r} is outside {NAMING_ROOT}")
    return path[len(NAMING_ROOT) + 1:]


def patched_item(item, patch):
    item = copy.deepcopy(item)
    for k, v in (patch or {}).items():
        item[k] = v
    return item


def naming_album(raw, inp):
    album = normalize(raw[f"album-get-{inp['album']}.json"])
    album = patched_item(album, inp["album_patch"])
    album["tracks"]["items"] = [patched_item(t, inp["track_patch"]) for t in album["tracks"]["items"]]
    return album


def run_naming(raw, inp):
    """Names an album's or a track's folder and files the way download_release
    and download_track do, without fetching anything."""
    s = dl_settings(inp["settings"])
    q = inp["quality"]
    fmt, _, bd, sr = inp["gate"]
    dl = Download(None, "id", NAMING_ROOT, q, folder_format=inp["folder_format"],
                  track_format=inp["track_format"], settings=s)
    paths = []
    if inp["kind"] == "album":
        album = naming_album(raw, inp)
        attrs = dl._get_album_attr(album, downloader._get_title(album), fmt, bd, sr)
        dl._determine_formats(album_meta=album, album_attr=attrs, tracks_meta=album["tracks"]["items"],
                              track_attr=None, is_track=False, file_format=fmt, settings=s)
        folder = downloader.process_folder_format_with_subdirs(
            dl.folder_format, attrs, NAMING_ROOT, s.legacy_charmap, s.disable_smart_truncation)
        multiple = album.get("media_count", 1) > 1
        with recording_fit_name(paths):
            for count, t in enumerate(album["tracks"]["items"]):
                dl._download_and_tag(folder, count, None, t, album, False, q == 5,
                                     t.get("media_number") if multiple else None)
    else:
        track = patched_item(normalize(raw[f"track-get-{inp['track']}.json"]), inp["track_patch"])
        attrs = dl._get_track_attr(track, downloader._get_title(track), bd, sr, fmt)
        dl._determine_formats(album_meta=track.get("album", {}), album_attr=None, tracks_meta=[track],
                              track_attr=attrs, is_track=True, file_format=fmt, settings=s)
        folder = downloader.process_folder_format_with_subdirs(
            dl.folder_format, attrs, NAMING_ROOT, s.legacy_charmap, s.disable_smart_truncation)
        with recording_fit_name(paths):
            dl._download_and_tag(folder, 1, None, track, track, True, q == 5, False)
    return {
        "formats": [dl.folder_format, dl.track_format, s.multiple_disc_track_format],
        "folder": relative(folder),
        "files": [relative(p) for p in paths],
    }


def naming_inputs(index, raw):
    fake = FakeClient(index, raw)
    items = index["items"]

    def inp(kind, ref, quality, gate, ff=BOT_FOLDER, tf=BOT_TRACK, settings=None, album_patch=None, track_patch=None):
        d = {"kind": kind, "quality": quality, "gate": list(gate), "folder_format": ff, "track_format": tf,
             "settings": settings or {}, "album_patch": album_patch or {}, "track_patch": track_patch or {}}
        d["album" if kind == "album" else "track"] = ref
        return d

    combos = [
        ("bot", {}),
        ("defaults", {"ff": None, "tf": None}),
        ("subfolders", {"ff": "{album_artist}/{album_title} ({year})/{release_type}{version_tag}",
                        "tf": "{disc_number}-{track_number} {track_title}{explicit}"}),
        ("one dir", {"settings": {"multiple_disc_one_dir": True}}),
        ("one dir with its template", {"settings": {"multiple_disc_one_dir": True,
                                                    "multiple_disc_track_format": "{disc_number}{track_number} {tracktitle}"}}),
        ("disc prefix", {"settings": {"multiple_disc_prefix": "Disc"}}),
        ("ValueError in the folder", {"ff": "{year:d} {album}"}),
        ("KeyError in the track", {"tf": "{nope} - {track_title}"}),
        ("KeyError in a folder part", {"ff": "{artist}/{nope} {album}/{year}"}),
        ("KeyError everywhere", {"ff": "{year:d}", "tf": "{nope}",
                                 "settings": {"fallback_folder_format": "{nope:d}", "multiple_disc_track_format": "{x}"}}),
        ("fallback folder", {"ff": "{year:d}", "settings": {"fallback_folder_format": "{album_artist}/{album}"}}),
        ("extensions in templates", {"ff": " {artist} - {album}.flac ", "tf": "{track_number}. {track_title}.mp3"}),
        ("legacy charmap", {"settings": {"legacy_charmap": True}}),
        ("no truncation", {"settings": {"disable_smart_truncation": True}}),
    ]
    for kind, album_id in items["albums"].items():
        album = normalize(raw[f"album-get-{album_id}.json"])
        gates = {"q27": (27, release_gate(fake, album, 27)), "q6": (6, release_gate(fake, album, 6)),
                 "q5": (5, release_gate(fake, album, 5)), "failed gate": (27, ("Unknown", None, None)),
                 "float rate": (27, ("FLAC", 24, 96.0))}
        for gname, (q, (fmt, bd, sr)) in gates.items():
            for cname, c in combos:
                if gname != "q27" and cname not in ("bot", "defaults"):
                    continue
                yield f"album/{kind} {gname} {cname}", inp("album", album_id, q, (fmt, True, bd, sr), **c)

    long_title = "A Very Long Album Title " * 8
    long_track = "An Extraordinarily Long Track Title, " * 6
    for cname, c in [("bot", {}), ("no truncation", {"settings": {"disable_smart_truncation": True}}),
                     ("subfolders", combos[2][1])]:
        yield f"album/long titles {cname}", inp(
            "album", items["albums"]["multi_disc"], 27, ("FLAC", True, 24, 192),
            album_patch={"title": long_title, "version": "Remastered"}, track_patch={"title": long_track}, **c)
    yield "album/media_number missing", inp("album", items["albums"]["multi_disc"], 27, ("FLAC", True, 24, 192),
                                            track_patch={"media_number": None})
    yield "album/track_number a string", inp("album", items["albums"]["cd"], 27, ("FLAC", True, 16, 44.1),
                                             track_patch={"track_number": "7"})
    yield "album/title None", inp("album", items["albums"]["cd"], 27, ("FLAC", True, 16, 44.1),
                                  album_patch={"title": None})
    yield "album/version in title", inp("album", items["albums"]["cd"], 27, ("FLAC", True, 16, 44.1),
                                        album_patch={"version": "COM LAG"}, track_patch={"version": "live"})

    track = normalize(raw[f"track-get-{items['track']}.json"])
    gates = {"q27": (27, track_gate(fake, track, 27)), "q5": (5, track_gate(fake, track, 5)),
             "failed gate": (27, ("Unknown", None, None)), "float rate": (27, ("FLAC", 24, 96.0))}
    for gname, (q, (fmt, bd, sr)) in gates.items():
        for cname, c in combos[:3] + combos[6:9]:
            yield f"track/{gname} {cname}", inp("track", items["track"], q, (fmt, True, bd, sr), **c)
    yield "track/album None", inp("track", items["track"], 27, ("FLAC", True, 24, 96), track_patch={"album": None})


def gen_naming(index, raw):
    g = Golden("naming")
    with quiet_downloader():
        for name, inp in naming_inputs(index, raw):
            g.case(name, inp, lambda inp=inp: run_naming(raw, inp))
    g.write()


def run_tracklist(inp, album):
    dl = Download(None, "id", NAMING_ROOT, 27, no_credits=inp["no_credits"], settings=QobuzDLSettings())
    fmt, bd, sr = inp["gate"]
    with tempfile.TemporaryDirectory() as tmp:
        dl._generate_tracklist(copy.deepcopy(album), tmp, inp["title"], fmt, bd, sr)
        files = sorted(Path(tmp).iterdir())
        return [{"name": f.name, "text": f.read_text(encoding="utf-8")} for f in files]


def tracklist_inputs(index, raw):
    fake = FakeClient(index, raw)
    for kind, album_id in index["items"]["albums"].items():
        album = normalize(raw[f"album-get-{album_id}.json"])
        yield f"album/{kind}", {"album": album_id, "title": downloader._get_title(album),
                                "gate": list(release_gate(fake, album, 27)), "no_credits": False}, album

    def t(n, **over):
        d = {"id": n, "title": f"Track {n}", "track_number": n, "media_number": 1, "duration": 61 * n,
             "performer": {"name": "Performer"}}
        d.update(over)
        return d

    base = {"title": "Album", "artist": {"name": "Artist"}, "label": {"name": "Label"},
            "genre": {"name": "Jazz"}, "release_date_original": "2001-02-03"}
    built = [
        ("one disc", {"tracks": {"items": [t(1), t(2, parental_warning=True), t(3, performers="A, Main - B, Guest\r\nC, Bass")]}}),
        ("discs", {"tracks": {"items": [t(1), t(2, media_number=2), t(3, media_number=2), t(4, media_number=3), t(5, media_number=1)]}}),
        ("disc numbers missing", {"tracks": {"items": [t(1), {"title": "No disc", "duration": 5}, t(3, media_number=2)]}}),
        ("nulls", {"artist": None, "composer": {"name": None}, "label": {"name": None}, "genre": {"name": None},
                   "release_date_original": None, "parental_warning": None,
                   "tracks": {"items": [t(1, title=None, track_number=None, performer=None, performers=None)]}}),
        ("missing keys", {"artist": {}, "label": {}, "genre": {},
                          "tracks": {"items": [{"duration": 0}, {"id": 2, "performer": {}}]}}),
        ("no tracks", {"tracks": {}}),
        ("tracks None", {"tracks": None}),
        ("duration None", {"tracks": {"items": [t(1), t(2, duration=None), t(3)]}}),
        ("duration a float and negative", {"tracks": {"items": [t(1, duration=125.9), t(2, duration=-61)]}}),
        ("genre mapped", {"genre": {"name": "Rock progressif"}, "composer": {"name": "Composer"},
                          "tracks": {"items": [t(1)]}}),
        ("long titles", {"tracks": {"items": [t(1, title="T" * 70), t(2, title="Ünïcödé 日本語 " * 6)]}}),
        ("description", {"description": "First paragraph with <b>bold</b> and a <a href=\"x\">link</a>.<br>Second "
                         + "word " * 40 + "<br/>\n\n<br />Third<p>para</p>  \n  ", "tracks": {"items": [t(1)]}}),
        ("description not a string", {"description": 12345, "tracks": {"items": [t(1)]}}),
        ("artist a string", {"artist": "Artist", "tracks": {"items": [t(1)]}}),
        ("performers a number", {"tracks": {"items": [t(1, performers=5)]}}),
        ("track a string", {"tracks": {"items": ["x"]}}),
    ]
    for name, over in built:
        album = dict(base)
        album.update(over)
        yield f"built/{name}", {"album": album, "title": "Album", "gate": ["FLAC", 24, 96], "no_credits": False}, album
    album = dict(base, tracks={"items": [t(1)]})
    yield "built/title with slashes", {"album": album, "title": "A/B: C?", "gate": ["FLAC", 16, 44.1], "no_credits": False}, album
    yield "built/title None", {"album": album, "title": None, "gate": ["FLAC", 16, 44.1], "no_credits": False}, album
    yield "built/gate failed", {"album": album, "title": "Album", "gate": ["Unknown", None, None], "no_credits": False}, album
    yield "built/no credits", {"album": album, "title": "Album", "gate": ["FLAC", 16, 44.1], "no_credits": True}, album


def gen_tracklist(index, raw):
    g = Golden("tracklist")
    with quiet_downloader():
        for name, inp, album in tracklist_inputs(index, raw):
            g.case(name, inp, lambda inp=inp, album=album: run_tracklist(inp, album))
    g.write()


STATIC_HOST = "https://static.qobuz.com/"


def rewrite_static(o, base):
    if isinstance(o, dict):
        return {k: rewrite_static(v, base) for k, v in o.items()}
    if isinstance(o, list):
        return [rewrite_static(v, base) for v in o]
    if isinstance(o, str) and o.startswith(STATIC_HOST):
        return base + "/static/" + o[len(STATIC_HOST):]
    return o


class LayoutHandler(BaseHTTPRequestHandler):
    """The CDN and static files of qobuzdltest: one file per format, a cover
    for .jpg and a booklet for .pdf, with Range answered by 206."""

    FORMATS = {5: "320.mp3", 6: "16-44.1.flac", 7: "24-96.flac", 27: "24-192.flac"}

    def log_message(self, *args):
        pass

    def do_GET(self):
        parts = self.path.split("?")[0].split("/")
        if self.path.startswith("/cdn/file/") and len(parts) == 5 and int(parts[4]) in self.FORMATS:
            name = self.FORMATS[int(parts[4])]
        elif self.path.startswith("/static/") and self.path.lower().endswith(".jpg"):
            name = "cover.jpg"
        elif self.path.startswith("/static/") and self.path.lower().endswith(".pdf"):
            name = "booklet.pdf"
        else:
            self.send_error(404)
            return
        data = (AUDIO / name).read_bytes()
        start = 0
        rng = self.headers.get("Range")
        if rng:
            start = int(rng.split("=")[1].split("-")[0])
            if start >= len(data):
                self.send_response(416)
                self.send_header("Content-Range", f"bytes */{len(data)}")
                self.send_header("Content-Length", "0")
                self.end_headers()
                return
            self.send_response(206)
            self.send_header("Content-Range", f"bytes {start}-{len(data) - 1}/{len(data)}")
        else:
            self.send_response(200)
        self.send_header("Content-Length", str(len(data) - start))
        self.end_headers()
        self.wfile.write(data[start:])


# The artist qobuzdltest builds from recorded albums, as ArtistAlbums.
ARTIST_ALBUMS_ID = 9000002
ARTIST_ALBUMS_NAME = "Norah Jones"
ARTIST_ALBUMS = ("hires_96", "with_version", "cd")


def track_sources(raw):
    """The tracks qobuzdltest answers track/get for that nothing recorded,
    found as its buildCatalog finds them: recorded track/get answers, then
    each album's tracks in file order, then the playlist's."""
    out = {}
    for name in sorted(raw):
        if name.startswith("track-get-"):
            out[str(raw[name]["id"])] = (raw[name], None)
    for name in sorted(raw):
        if name.startswith("album-get-") and isinstance(raw[name].get("tracks"), dict):
            for t in raw[name]["tracks"].get("items", []):
                out.setdefault(str(t["id"]), (t, raw[name]))
    for name in sorted(raw):
        if name.startswith("playlist-get-"):
            for t in raw[name]["tracks"]["items"]:
                out.setdefault(str(t["id"]), (t, None))
    return out


def built_track_get(item, album):
    """qobuzdltest's track/get for a listed track: the album's track with the
    album less its tracks in front, or the playlist's track less the keys
    only a playlist has."""
    item = copy.deepcopy(item)
    if album is None:
        for k in ("position", "created_at", "playlist_track_id"):
            item.pop(k, None)
        return item
    album = copy.deepcopy(album)
    album.pop("tracks", None)
    return {"album": album, **item}


def artist_albums(index, raw):
    """qobuzdltest's ArtistAlbums: the recorded artist's first page listing
    recorded albums, as album/get answers them less their tracks."""
    page = copy.deepcopy(raw[f"artist-get-{index['items']['artist']}-0.json"])
    items = []
    for kind in ARTIST_ALBUMS:
        a = copy.deepcopy(raw[f"album-get-{index['items']['albums'][kind]}.json"])
        a.pop("tracks", None)
        items.append(a)
    page["albums"]["items"] = items
    page["albums"]["total"] = len(items)
    page["id"] = ARTIST_ALBUMS_ID
    page["albums_count"] = len(items)
    page["name"] = ARTIST_ALBUMS_NAME
    return page


class LayoutClient:
    """Answers as qobuzdltest does: the recorded answers, static URLs on the
    local server, and getFileUrl URLs naming the delivered format."""

    def __init__(self, index, raw, base):
        self.index, self.raw, self.base = index, raw, base
        self.urls = FakeClient(index, raw).urls
        self.tracks = track_sources(raw)

    def answer(self, d):
        return rewrite_static(normalize(d), self.base)

    def get_album_meta(self, id):
        return self.answer(self.raw[f"album-get-{id}.json"])

    def get_track_meta(self, id):
        name = f"track-get-{id}.json"
        if name in self.raw:
            return self.answer(self.raw[name])
        return self.answer(built_track_get(*self.tracks[str(id)]))

    def multi_meta(self, epoint, key, id):
        """qopy's multi_meta over the fake's pages: each page the whole
        collection with its items cut by offset and limit."""
        if epoint == "artist/get" and str(id) == str(ARTIST_ALBUMS_ID):
            whole = artist_albums(self.index, self.raw)
        else:
            whole = self.raw[f"{epoint.replace('/', '-')}-{id}-0.json"]
        items_key = "tracks" if "playlist" in epoint else "albums"
        offset = 0
        while True:
            page = copy.deepcopy(whole)
            sub = page[items_key]
            sub["offset"], sub["limit"] = offset, 50
            sub["items"] = sub["items"][offset:offset + 50]
            res = self.answer(page)
            items = res.get(items_key, {}).get("items", [])
            if not items:
                break
            yield res
            offset += len(items)
            if offset >= res.get(items_key, {}).get("total", res.get(key, 0)):
                break

    def get_plist_meta(self, id):
        return self.multi_meta("playlist/get", "tracks_count", id)

    def get_artist_meta(self, id):
        return self.multi_meta("artist/get", "albums_count", id)

    def get_label_meta(self, id):
        return self.multi_meta("label/get", "albums_count", id)

    def get_track_url(self, id, fmt_id, force_segments=False):
        if force_segments:
            raise ConnectionError("the segmented path is not served")
        ans = copy.deepcopy(self.urls[(str(id), int(fmt_id))])
        ans["url"] = f"{self.base}/cdn/file/{id}/{ans['format_id']}?etsp=946684800&hmac=qobuzdltest-cdn-signature"
        return ans


def layout_tree(root):
    out = []
    for p in sorted(Path(root).rglob("*"), key=lambda p: p.relative_to(root).as_posix()):
        rel = p.relative_to(root).as_posix()
        if p.is_dir():
            out.append({"path": rel, "type": "dir"})
        elif p.suffix == ".flac":
            out.append({"path": rel, "type": "flac", "result": flac_result(p)})
        elif p.suffix == ".mp3":
            out.append({"path": rel, "type": "mp3", "result": mp3_result(p)})
        elif p.suffix in (".txt", ".m3u8"):
            out.append({"path": rel, "type": "text", "text": p.read_text(encoding="utf-8")})
        else:
            out.append({"path": rel, "type": "file", "sha256": sha(p.read_bytes())})
    return out


def run_layout(client, inp):
    o = inp["options"]
    s = dl_settings(o["settings"])
    with tempfile.TemporaryDirectory() as tmp, open(os.devnull, "w") as null, contextlib.redirect_stderr(null):
        dl = Download(client, inp["id"], tmp, o["quality"], o["embed_art"], o["albums_only"], o["fallback"],
                      False, False, o["folder_format"], o["track_format"], False, False, None,
                      o["no_credits"], s, None)
        downloader.abort_event.clear()
        dl.download_id_by_type(track=inp["kind"] == "track")
        return layout_tree(tmp)


def layout_inputs(index, kind):
    def inp(id, quality=27, fallback=True, embed_art=False, albums_only=False, no_credits=False,
            ff=BOT_FOLDER, tf=BOT_TRACK, settings=None):
        return {"kind": kind, "id": id, "options": {
            "quality": quality, "fallback": fallback, "embed_art": embed_art, "albums_only": albums_only,
            "no_credits": no_credits, "folder_format": ff, "track_format": tf, "settings": settings or {}}}

    items = index["items"]
    if kind == "track":
        t = items["track"]
        yield "bot", inp(t)
        yield "q5", inp(t, quality=5)
        yield "q6", inp(t, quality=6)
        yield "defaults", inp(t, ff=None, tf=None)
        yield "embedded cover", inp(t, embed_art=True, settings={"embed_art": True})
        yield "no cover", inp(t, settings={"no_cover": True})
        yield "no fallback", inp(t, fallback=False)
        return
    albums = items["albums"]
    for name, id in albums.items():
        yield f"{name} bot", inp(id)
    m = albums["multi_disc"]
    yield "multi_disc q5", inp(m, quality=5)
    yield "multi_disc q6", inp(m, quality=6)
    yield "multi_disc defaults", inp(m, ff=None, tf=None)
    yield "multi_disc one dir", inp(m, settings={"multiple_disc_one_dir": True})
    yield "multi_disc embedded cover", inp(m, embed_art=True, settings={"embed_art": True, "embedded_art_size": "300"})
    yield "multi_disc no credits, no cover", inp(m, no_credits=True, settings={"no_cover": True})
    yield "multi_disc saved art 600", inp(m, settings={"saved_art_size": "600"})
    yield "multi_disc one worker", inp(m, settings={"max_workers": 1})
    yield "hires_96 no fallback", inp(albums["hires_96"], fallback=False)
    yield "hires_96 no fallback q7", inp(albums["hires_96"], quality=7, fallback=False)
    yield "cd q27 no fallback", inp(albums["cd"], fallback=False)
    yield "compilation albums only", inp(albums["compilation"], albums_only=True)
    yield "cd albums only", inp(albums["cd"], albums_only=True)
    yield "goodies subfolders", inp(albums["goodies"], ff="{album_artist}/{album_title}", tf="{disc_number}.{track_number} {track_title}")


def gen_layout(index, raw):
    # The downloader's sleeps are a second per track; a short one keeps the
    # wait loops from spinning.
    real_time = downloader.time
    downloader.time = SimpleNamespace(sleep=lambda s: real_time.sleep(min(s, 0.002)))
    srv = ThreadingHTTPServer(("127.0.0.1", 0), LayoutHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        client = LayoutClient(index, raw, f"http://127.0.0.1:{srv.server_address[1]}")
        with quiet_downloader():
            for kind in ("album", "track"):
                g = Golden(f"layout_{kind}")
                for name, inp in layout_inputs(index, kind):
                    g.case(name, inp, lambda inp=inp: run_layout(client, inp))
                g.write()
    finally:
        srv.shutdown()
        downloader.time = real_time


# --- Core: collections, m3u8, search, downloads database -------------------


def run_core_layout(client, inp):
    """handle_url on a collection, with a downloads database: the tree, and
    the rows with saved_path relative to the download folder."""
    o = inp["options"]
    s = dl_settings(o["settings"])
    with tempfile.TemporaryDirectory() as tmp, tempfile.TemporaryDirectory() as dbdir, \
            open(os.devnull, "w") as null, contextlib.redirect_stderr(null):
        dbpath = os.path.join(dbdir, "qobuz_dl.db")
        qdl = QobuzDL(directory=tmp, quality=o["quality"], embed_art=o["embed_art"],
                      ignore_singles_eps=o["albums_only"], no_m3u_for_playlists=o["no_m3u"],
                      quality_fallback=o["fallback"], downloads_db=dbpath, folder_format=o["folder_format"],
                      track_format=o["track_format"], smart_discography=o["smart"], no_credits=o["no_credits"],
                      settings=s)
        qdl.client = client
        downloader.abort_event.clear()
        with sorted_walk():
            qdl.handle_url(f"https://play.qobuz.com/{inp['kind']}/{inp['id']}")
        gc_sqlite()
        rows = db_dump(dbpath)["rows"]
        for r in rows:
            if r["saved_path"] != tmp and not r["saved_path"].startswith(tmp + "/"):
                raise ValueError(f"saved_path {r['saved_path']!r} is outside {tmp}")
            r["saved_path"] = r["saved_path"][len(tmp) + 1:]
        return {"tree": layout_tree(tmp), "db": rows}


def core_layout_inputs(index, kind):
    def inp(id, quality=27, fallback=True, embed_art=False, albums_only=False, no_credits=False, no_m3u=False,
            smart=False, ff=BOT_FOLDER, tf=BOT_TRACK, settings=None):
        return {"kind": kind, "id": str(id), "options": {
            "quality": quality, "fallback": fallback, "embed_art": embed_art, "albums_only": albums_only,
            "no_credits": no_credits, "no_m3u": no_m3u, "smart": smart, "folder_format": ff, "track_format": tf,
            "settings": settings or {}}}

    if kind == "playlist":
        p = index["items"]["playlist"]
        yield "bot", inp(p)
        yield "q5", inp(p, quality=5)
        yield "q6", inp(p, quality=6)
        yield "no m3u", inp(p, no_m3u=True)
        yield "no fallback q7", inp(p, quality=7, fallback=False)
        yield "embedded cover", inp(p, embed_art=True, settings={"embed_art": True})
        yield "defaults", inp(p, ff=None, tf=None)
        yield "one worker", inp(p, settings={"max_workers": 1})
        return
    yield "bot", inp(ARTIST_ALBUMS_ID)
    yield "smart discography", inp(ARTIST_ALBUMS_ID, smart=True)
    yield "smart discography q6", inp(ARTIST_ALBUMS_ID, smart=True, quality=6)


def gen_core_layout(index, raw):
    real_time = downloader.time
    downloader.time = SimpleNamespace(sleep=lambda s: real_time.sleep(min(s, 0.002)))
    srv = ThreadingHTTPServer(("127.0.0.1", 0), LayoutHandler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    try:
        client = LayoutClient(index, raw, f"http://127.0.0.1:{srv.server_address[1]}")
        with quiet_downloader():
            for kind in ("playlist", "artist"):
                g = Golden(f"layout_{kind}")
                for name, inp in core_layout_inputs(index, kind):
                    g.case(name, inp, lambda inp=inp: run_core_layout(client, inp))
                g.write()
    finally:
        srv.shutdown()
        downloader.time = real_time


@contextlib.contextmanager
def sorted_walk():
    """os.walk with each folder's files sorted by name, as the port lists
    them; Python's order is the file system's."""
    real = os.walk

    def walk(top, *a, **k):
        for root, dirs, files in real(top, *a, **k):
            files.sort()
            yield root, dirs, files

    os.walk = walk
    try:
        yield
    finally:
        os.walk = real


M3U = FIXTURES / "m3u"
# Offsets in 320-bare.mp3's first frame: the Info header at 36, its frame
# count after the flags at 44, and the encoder string after the byte count,
# the TOC and the VBR scale at 156.
MP3_XING, MP3_FRAMES, MP3_ENCODER = 36, 44, 156


def mp3_variant(kind):
    data = bytearray((AUDIO / "320-bare.mp3").read_bytes())
    if data[MP3_XING:MP3_XING + 4] != b"Info" or data[MP3_ENCODER:MP3_ENCODER + 4] != b"Lavc":
        raise ValueError("320-bare.mp3 has moved its Info header")
    if kind == "frames":
        # 40,000 frames of 1,152 samples at 44.1 kHz: 1,044.9 seconds.
        data[MP3_FRAMES:MP3_FRAMES + 4] = struct.pack(">I", 40000)
    elif kind == "lame":
        # 3,829 frames are 100.02 seconds; LAME's delay and padding of 2,076
        # samples take it under 100.
        data[MP3_FRAMES:MP3_FRAMES + 4] = struct.pack(">I", 3829)
        lame = bytearray(27)
        lame[12:15] = bytes([576 >> 4, (576 & 0xF) << 4 | 1500 >> 8, 1500 & 0xFF])
        data[MP3_ENCODER:MP3_ENCODER + 36] = b"LAME3.100" + bytes(lame)
    elif kind == "cbr":
        # No frame count, so the length comes from the size at 32 kbps: 1.57 s.
        data[2] = 0x10
        data[MP3_XING + 4:MP3_XING + 8] = struct.pack(">I", 0x0E)
    elif kind == "vbri":
        # 5,000 frames: 130.6 seconds.
        vbri = b"VBRI" + struct.pack(">HHHIIHHHH", 1, 0, 50, 1_000_000, 5000, 0, 1, 2, 1)
        data[MP3_XING:MP3_XING + len(vbri)] = vbri
    elif kind != "bare":
        raise ValueError(kind)
    return bytes(data)


def flac_variant(src, samples=None):
    prefix, blocks, audio = flac_parts((AUDIO / src).read_bytes())
    if samples is not None:
        info = bytearray(blocks[0][1])
        info[13] = (info[13] & 0xF0) | (samples >> 32)
        info[14:18] = struct.pack(">I", samples & 0xFFFFFFFF)
        blocks[0] = (0, bytes(info))
    return flac_bytes(blocks, audio, prefix)


def tag_flac_file(path, tags):
    f = FLAC(str(path))
    for k, v in tags.items():
        f[k] = v
    f.save()


def tag_mp3_file(path, frames, version=3):
    t = id3.ID3()
    for f in frames:
        t.add(f)
    t.save(str(path), v2_version=version)


def write_m3u_fixtures():
    """The audio files the m3u8 cases put in their folders: tagged copies of
    the fixtures, with lengths of whole seconds, and broken files."""
    shutil.rmtree(M3U, ignore_errors=True)
    M3U.mkdir(parents=True)

    def flac(name, tags=None, src="16-44.1.flac", samples=None):
        (M3U / name).write_bytes(flac_variant(src, samples))
        if tags:
            tag_flac_file(M3U / name, tags)

    def mp3(name, kind, frames=None, version=3, prefix=b""):
        (M3U / name).write_bytes(prefix + mp3_variant(kind))
        if frames:
            tag_mp3_file(M3U / name, frames, version)

    flac("alpha.flac", {"QOBUZTRACKID": "101", "ISRC": "ISRC00000101", "TITLE": "Alpha", "ARTIST": "Artist A"},
         samples=44100 * 61 + 44099)
    flac("beta.flac", {"QOBUZTRACKID": "102", "TITLE": "Beta", "ARTIST": "Artist B"}, src="24-96.flac")
    flac("gamma.flac", {"ISRC": "ISRC00000103", "TITLE": "Gamma", "ARTIST": "Artist C"}, src="24-192.flac",
         samples=192000 * 3600)
    flac("delta.flac", {"TITLE": "  Delta Title  "})
    flac("lower.flac", {"qobuztrackid": "105", "title": "Épsilon", "artist": "Ärtist E"})
    flac("multi.flac", {"QOBUZTRACKID": ["106", "107"], "TITLE": ["Zeta", "Second"], "ARTIST": ["One", "Two"]})
    flac("alpha-again.flac", {"TITLE": "alpha", "ARTIST": "Another"})
    flac("untagged.flac", src="no-comments.flac")
    flac("prefixed.flac", src="id3-prefixed.flac", samples=44100 * 2)
    (M3U / "bad.flac").write_bytes(b"fLaC but not really" * 4)
    mp3("omega.mp3", "frames", [id3.TXXX(encoding=1, desc="QOBUZTRACKID", text="201"),
                                id3.TSRC(encoding=0, text="ISRC00000201"), id3.TIT2(encoding=1, text="Ωmega"),
                                id3.TPE1(encoding=1, text="Artíst M")])
    mp3("lame.mp3", "lame", [id3.TXXX(encoding=0, desc="qobuztrackid", text="202"),
                             id3.TIT2(encoding=0, text="Lame"), id3.TPE1(encoding=0, text="Encoder")])
    mp3("cbr.mp3", "cbr", [id3.TIT2(encoding=0, text="Cbr")])
    mp3("vbri.mp3", "vbri", [id3.TIT2(encoding=3, text="Vbri"), id3.TPE1(encoding=3, text=["One", "Two"])], 4)
    mp3("notitle.mp3", "frames", [id3.TSSE(encoding=0, text="Lavf")])
    mp3("bare.mp3", "bare")
    # A false sync whose bitrate is invalid, then zeros, then the frames.
    mp3("junk.mp3", "frames", [id3.TIT2(encoding=0, text="Junk")], prefix=b"\xff\xfb\x00\x00" + bytes(300))
    # make_m3u takes the length only from a file with tags: mutagen's
    # FileType is false without any.
    mp3("untagged.mp3", "frames")
    flac("untagged-long.flac", src="no-comments.flac", samples=44100 * 10)
    flac("empty-tags.flac", src="no-comments.flac", samples=44100 * 10)
    f = FLAC(str(M3U / "empty-tags.flac"))
    f.add_tags()
    f.save()
    (M3U / "garbage.mp3").write_bytes(b"not an mp3 at all " * 8)
    (M3U / "empty.mp3").write_bytes(b"")
    (M3U / "cover.jpg").write_bytes((AUDIO / "cover.jpg").read_bytes())


def run_m3u(inp):
    with tempfile.TemporaryDirectory() as tmp:
        folder = Path(tmp) / inp["dir"]
        folder.mkdir()
        for dest, src in inp["files"].items():
            (folder / dest).parent.mkdir(parents=True, exist_ok=True)
            shutil.copyfile(M3U / src, folder / dest)
        with sorted_walk():
            utils.make_m3u(str(folder), copy.deepcopy(inp["items"]))
        written = sorted(p.name for p in folder.iterdir() if p.suffix == ".m3u8")
        if not written:
            return None
        if written != [inp["dir"] + ".m3u8"]:
            raise ValueError(f"unexpected playlist files {written}")
        return (folder / written[0]).read_text(encoding="utf-8")


def m3u_inputs():
    def inp(files, items=None, dir="My Playlist"):
        if isinstance(files, list):
            files = {f: f for f in files}
        return {"dir": dir, "files": files, "items": items}

    def item(id, title, isrc=None, artist="Album Artist", performer="Performer", **more):
        out = {"id": id, "title": title}
        if isrc is not None:
            out["isrc"] = isrc
        out["performer"] = {"name": performer}
        out["album"] = {"artist": {"name": artist}}
        out.update(more)
        return out

    for name in sorted(p.name for p in M3U.iterdir() if p.suffix in (".flac", ".mp3")):
        yield f"length and tags: {name}", inp([name])
    four = ["alpha.flac", "beta.flac", "gamma.flac", "omega.mp3"]
    yield "natural order without items", inp(four)
    yield "natural order with an empty list", inp(four, [])
    yield "by track ID", inp(four, [item(201, "x"), item(102, "x"), item(101, "x")])
    yield "track ID as a string", inp(four, [item("102", "x")])
    yield "by ISRC", inp(four, [item(1, "x", isrc="ISRC00000103"), item(2, "x", isrc="ISRC00000201")])
    yield "ISRC missing is the text None", inp(four, [item(1, "x")])
    yield "by title, stripped and lower case", inp(
        ["delta.flac", "beta.flac"], [item(1, "  delta title "), item(2, "BETA")])
    yield "ID before ISRC before title", inp(
        four, [item(102, "Gamma", isrc="ISRC00000101"), item(9, "Gamma", isrc="ISRC00000101"), item(9, "Gamma")])
    yield "by file name", inp({"01 - Some Song (Live).flac": "beta.flac", "02 - Other.flac": "delta.flac"},
                              [item(1, "some song"), item(2, "OTHER")])
    yield "file name match takes the first file", inp(
        {"a Song.flac": "untagged.flac", "b song.flac": "untagged.flac"}, [item(1, "song")])
    yield "duplicates kept", inp(four, [item(101, "x"), item(101, "x"), item(102, "x"), item(101, "x")])
    yield "nothing matched falls back to natural order", inp(four, [item(9, "nope"), item(8, "none")])
    yield "misses between matches", inp(four, [item(9, "nope"), item(102, "x"), item(8, "Missing", artist="Various Artists")])
    yield "title missing is Unknown Title", inp(four, [{"id": 9}, item(101, "x")])
    yield "title None", inp(four, [{"id": 9, "title": None}])
    yield "album None", inp(four, [{"id": 101, "title": "x", "album": None}])
    yield "item with no album or performer", inp(four, [{"id": 9, "title": "nope"}, item(102, "x")])
    yield "empty title matches any file name", inp(four, [{"id": 9, "title": ""}])
    yield "later file wins a title clash", inp(["alpha.flac", "alpha-again.flac"], [item(9, "ALPHA")])
    yield "case-insensitive Vorbis keys", inp(["lower.flac", "beta.flac"], [item(105, "x"), item(9, "épsilon")])
    yield "multiple values take the first", inp(["multi.flac"], [item(107, "x"), item(106, "x"), item(9, "second")])
    yield "MP3 TXXX description ignores case", inp(["lame.mp3", "omega.mp3"], [item(202, "x"), item(201, "x")])
    yield "broken files fall back to their names", inp(
        ["bad.flac", "garbage.mp3", "empty.mp3", "bare.mp3"], [item(9, "garbage"), item(9, "bad"), item(9, "bare")])
    yield "subfolders sorted", inp({"b/2.flac": "beta.flac", "a/1.flac": "alpha.flac", "c.flac": "gamma.flac",
                                    "a/z/0.flac": "delta.flac"})
    yield "subfolders with items", inp({"b/2.flac": "beta.flac", "a/1.flac": "alpha.flac"},
                                       [item(102, "x"), item(101, "x")])
    yield "extensions in any case", inp({"one.FLAC": "alpha.flac", "two.Mp3": "omega.mp3", "three.flac.txt": "beta.flac",
                                         "cover.jpg": "cover.jpg"})
    yield "numbers sort by value", inp({"track 10.flac": "alpha.flac", "track 2.flac": "beta.flac",
                                        "Track 1.flac": "gamma.flac", "track 02.flac": "delta.flac",
                                        "track \u0663.flac": "untagged.flac", "track.flac": "multi.flac"})
    yield "a digit Python can't read as a number", inp({"1\u00b22.flac": "alpha.flac", "b.flac": "beta.flac"})
    yield "only other files", inp({"cover.jpg": "cover.jpg"})
    yield "empty folder", inp({})
    yield "folder name with spaces and accents", inp(["alpha.flac"], dir="Café del Mar: Vol. 1")


def gen_m3u():
    g = Golden("m3u")
    for name, inp in m3u_inputs():
        g.case(name, inp, lambda inp=inp: run_m3u(inp))
    g.write()


class SearchClient:
    """Answers each search with the given items."""

    def __init__(self, items):
        self.items = items

    def _answer(self, key):
        return {key: {"items": copy.deepcopy(self.items)}}

    def search_albums(self, query, limit=20):
        return self._answer("albums")

    def search_tracks(self, query, limit=20):
        return self._answer("tracks")

    def search_artists(self, query, limit=20):
        return self._answer("artists")

    def search_playlists(self, query, limit=20):
        return self._answer("playlists")

    def get_favorites(self, fav_type="albums", limit=100, offset=0):
        raise NotImplementedError("favorites are not ported")


def run_search(inp):
    me = SimpleNamespace(client=SearchClient(inp["items"]))
    return QobuzDL.search_by_type(me, "query", inp["type"], 10)


def search_inputs(raw):
    for typ in ("albums", "tracks", "artists", "playlists"):
        items = normalize(raw[f"catalog-search-{typ}.json"])[typ]["items"]
        yield f"recorded {typ}", {"type": typ[:-1], "items": items}

    def album(**over):
        out = {"id": "a1", "title": "Title", "artist": {"name": "Artist"}, "release_date_original": "2020-05-06",
               "release_type": "album", "hires_streamable": False}
        for k, v in over.items():
            if v is MISSING:
                out.pop(k, None)
            else:
                out[k] = v
        return out

    guesses = [
        ("no type, long", dict(release_type=MISSING, duration=1740, tracks_count=2)),
        ("no type, 1739 seconds", dict(release_type=MISSING, duration=1739, tracks_count=2)),
        ("no type, 7 tracks", dict(release_type=MISSING, duration=10, tracks_count=7)),
        ("no type, 1 track", dict(release_type=MISSING, duration=200, tracks_count=1)),
        ("no type, nothing known", dict(release_type=MISSING)),
        ("no type, zeros", dict(release_type=MISSING, duration=0, tracks_count=0)),
        ("no type, duration only", dict(release_type=MISSING, duration=100)),
        ("no type, float duration", dict(release_type=MISSING, duration=1740.0, tracks_count=0)),
        ("empty type", dict(release_type="", tracks_count=1)),
        ("product type", dict(release_type=None, product_type="single")),
        ("ep", dict(release_type="ep")),
        ("EP upper", dict(release_type="EP")),
        ("epmini", dict(release_type="epmini")),
        ("two words", dict(release_type="live album")),
        ("hi-res", dict(hires_streamable=True, maximum_bit_depth=24, maximum_sampling_rate=192)),
        ("hi-res float", dict(hires_streamable=True, maximum_bit_depth=24, maximum_sampling_rate=44.1)),
        ("hi-res defaults", dict(hires_streamable=True)),
        ("hi-res None", dict(hires_streamable=True, maximum_bit_depth=None, maximum_sampling_rate=None)),
        ("version and explicit", dict(version="Deluxe", parental_warning=True)),
        ("performer", dict(artist=MISSING, performer={"name": "Performer"})),
        ("artist without name", dict(artist={}, performer={"name": "Performer"})),
        ("unknown artist", dict(artist=MISSING)),
        ("name for title", dict(title=MISSING, name="Name")),
        ("unknown title", dict(title=None)),
        ("release date", dict(release_date_original=None, release_date="1999-12-31")),
        ("no date", dict(release_date_original=MISSING)),
        ("int date", dict(release_date_original=2001)),
        ("short date", dict(release_date_original="99")),
        ("long names", dict(artist={"name": "An Artist Name Of Twenty"}, title="T" * 35, release_type="compilation")),
        ("exact widths", dict(artist={"name": "A" * 20}, title="B" * 35, release_type="soundtra")),
        ("one over", dict(artist={"name": "A" * 21}, title="B" * 36, release_type="soundtrac")),
        ("wide characters", dict(artist={"name": "Ω" * 25}, title="日本語" * 13)),
        ("no id", dict(id=MISSING)),
        ("int id", dict(id=123)),
    ]
    for name, over in guesses:
        yield f"album: {name}", {"type": "album", "items": [album(**over)]}
    yield "album: several", {"type": "album", "items": [album(), album(id="a2", title="Second")]}
    yield "album: none", {"type": "album", "items": []}
    yield "track: no type", {"type": "track", "items": [album(release_type=MISSING, duration=3000, tracks_count=9)]}
    yield "track: performer", {"type": "track", "items": [album(artist=MISSING, performer={"name": "P"})]}
    yield "album: artist None", {"type": "album", "items": [album(artist=None)]}
    yield "album: type not a string", {"type": "album", "items": [album(release_type=5)]}
    yield "album: None duration", {"type": "album", "items": [album(release_type=MISSING, tracks_count=3, duration=None)]}
    yield "album: string count", {"type": "album", "items": [album(release_type=MISSING, tracks_count="7")]}

    def named(**over):
        out = {"id": 7, "name": "Name"}
        out.update(over)
        return out

    for typ in ("artist", "playlist"):
        yield f"{typ}: albums", {"type": typ, "items": [named(albums_count=12)]}
        yield f"{typ}: tracks", {"type": typ, "items": [named(tracks_count=3)]}
        yield f"{typ}: albums None", {"type": typ, "items": [named(albums_count=None, tracks_count=3)]}
        yield f"{typ}: no counts", {"type": typ, "items": [{"id": 1}]}
        yield f"{typ}: long name", {"type": typ, "items": [named(name="N" * 51, tracks_count=1)]}
        yield f"{typ}: name None", {"type": typ, "items": [named(name=None, tracks_count=1)]}


def gen_search_text(raw):
    g = Golden("search_text")
    for name, inp in search_inputs(raw):
        g.case(name, inp, lambda inp=inp: run_search(inp))
    g.write()


DB = FIXTURES / "db"
V1_SCHEMA = "CREATE TABLE downloads (id TEXT UNIQUE NOT NULL);"
# db.py's table before the artist and album columns.
V2_SCHEMA = """
                CREATE TABLE downloads (
                  "id" text NOT NULL,
                  "media_type" text NOT NULL DEFAULT 'album',
                  "quality" integer NOT NULL DEFAULT 27,
                  "file_format" text NOT NULL DEFAULT 'FLAC',
                  "quality_met" integer NOT NULL DEFAULT 0,
                  "bit_depth" text,
                  "sampling_rate" text,
                  "saved_path" text NOT NULL DEFAULT '',
                  "status" text NOT NULL DEFAULT 'downloaded',
                  "url" text NOT NULL DEFAULT '',
                  "release_date" text NOT NULL DEFAULT '',
                  PRIMARY KEY ("id", "quality")
                );
                """

# Rows as the downloader adds them: handle_download_id's keyword arguments.
DB_ALBUM = {"item_id": "evz7usmruz35a", "media_type": "album", "quality": 27, "file_format": "FLAC",
            "quality_met": True, "bit_depth": 24, "sampling_rate": 192.0,
            "saved_path": "/music/Pink Floyd - Ummagumma (1969) [24B-192.0kHz]", "status": "downloaded",
            "url": "https://www.qobuz.com/album/evz7usmruz35a", "release_date": "1969-11-07",
            "artist": "Pink Floyd", "album": "Ummagumma"}
DB_TRACK = {"item_id": "20273753", "media_type": "track", "quality": 27, "file_format": "FLAC",
            "quality_met": False, "bit_depth": 24, "sampling_rate": 96.0,
            "saved_path": "/music/Miles Davis - So What (1959) [24B-96.0kHz]", "status": "downloaded",
            "url": "https://www.qobuz.com/album/x", "release_date": "1959-08-17",
            "artist": "Miles Davis", "album": "So What"}


def db_dump(path):
    conn = sqlite3.connect(path)
    try:
        schema = [list(r) for r in conn.execute(
            "SELECT type, name, tbl_name, sql FROM sqlite_master ORDER BY type, name")]
        columns = [list(r) for r in conn.execute("PRAGMA table_info(downloads)")]
        names = [c[1] for c in columns]
        rows = [dict(zip(names, r)) for r in conn.execute("SELECT * FROM downloads ORDER BY id, quality")]
        return {"schema": schema, "columns": columns, "rows": rows}
    finally:
        conn.close()


def build_db(path, schema, rows):
    path.unlink(missing_ok=True)
    conn = sqlite3.connect(path)
    try:
        conn.execute(schema)
        for r in rows:
            cols = ", ".join(r)
            conn.execute(f"INSERT INTO downloads ({cols}) VALUES ({', '.join('?' * len(r))})", tuple(r.values()))
        conn.commit()
    finally:
        conn.close()


def write_db_fixtures():
    DB.mkdir(parents=True, exist_ok=True)
    build_db(DB / "v1.db", V1_SCHEMA, [{"id": "0634904080853"}, {"id": "evz7usmruz35a"}, {"id": "20273753"}])
    build_db(DB / "v2.db", V2_SCHEMA, [
        {"id": "evz7usmruz35a", "media_type": "album", "quality": 27, "file_format": "FLAC", "quality_met": 1,
         "bit_depth": "24", "sampling_rate": "192.0", "saved_path": "/music/Ummagumma", "url": "https://x",
         "release_date": "1969-11-07"},
        {"id": "20273753", "media_type": "track", "quality": 6, "file_format": "FLAC", "quality_met": 0,
         "bit_depth": None, "sampling_rate": None}])
    shared = DB / "shared.db"
    shared.unlink(missing_ok=True)
    db.create_db(str(shared))
    for rec in (DB_ALBUM, DB_TRACK, dict(DB_ALBUM, quality=6, bit_depth=16, sampling_rate=44.1)):
        rec = dict(rec)
        db.handle_download_id(str(shared), rec.pop("item_id"), add_id=True, **rec)
    gc_sqlite()


def gc_sqlite():
    # db.py's `with sqlite3.connect()` commits but never closes; collecting
    # the connections releases the files.
    import gc
    gc.collect()


def run_db_calls(calls):
    with tempfile.TemporaryDirectory() as tmp:
        path = str(Path(tmp) / "qobuz_dl.db")
        db.create_db(path)
        has = []
        for c in calls:
            c = dict(c)
            if c.pop("add"):
                db.handle_download_id(path, c.pop("item_id"), add_id=True, **c)
            else:
                has.append(db.handle_download_id(path, c["item_id"], quality=c["quality"]) is not None)
        gc_sqlite()
        return dict(db_dump(path), has=has)


def db_row_inputs():
    def add(base, **over):
        out = dict(base, add=True)
        out.update(over)
        return out

    def has(id, quality, media_type="album"):
        return {"add": False, "item_id": id, "quality": quality, "media_type": media_type}

    yield "new database", []
    yield "album and track", [add(DB_ALBUM), add(DB_TRACK), has("evz7usmruz35a", 27), has("20273753", 27),
                              has("evz7usmruz35a", 6), has("missing", 27)]
    yield "media type ignored", [add(DB_TRACK), has("20273753", 27, "album")]
    yield "duplicate", [add(DB_ALBUM), add(DB_ALBUM, artist="Other"), has("evz7usmruz35a", 27)]
    yield "same ID, other quality", [add(DB_ALBUM), add(DB_ALBUM, quality=6), has("evz7usmruz35a", 6)]
    yield "values", [
        add(DB_ALBUM, item_id="a", bit_depth=16, sampling_rate=44.1, quality_met=False),
        add(DB_ALBUM, item_id="b", bit_depth=None, sampling_rate=None, file_format="Unknown"),
        add(DB_ALBUM, item_id="c", bit_depth="24", sampling_rate="96", file_format="MP3"),
        add(DB_ALBUM, item_id="d", bit_depth=24, sampling_rate=88.2),
        add(DB_ALBUM, item_id="e", bit_depth=24, sampling_rate=176.4),
        add(DB_ALBUM, item_id="f", bit_depth=24, sampling_rate=0.1 + 0.2),
        add(DB_ALBUM, item_id="g", bit_depth=24, sampling_rate=22.05, artist="Ünïcode, Ärtists", album="日本"),
        add(DB_ALBUM, item_id="h", saved_path="", url="", release_date="", artist="", album=""),
    ]


def gen_db():
    g = Golden("db_rows")
    for name, calls in db_row_inputs():
        g.case(name, {"calls": calls}, lambda calls=calls: run_db_calls(calls))
    g.case("shared file", {"file": "shared.db"}, lambda: db_dump(DB / "shared.db"))
    g.write()

    def migrate(name):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "qobuz_dl.db"
            if name is not None:
                shutil.copyfile(DB / name, path)
            db.create_db(str(path))
            gc_sqlite()
            return db_dump(path)

    g = Golden("db_migrations")
    for name in ("v1.db", "v2.db", "shared.db", None):
        g.case(name or "no file", {"file": name}, lambda name=name: migrate(name))
    g.write()


# --- Segments -------------------------------------------------------------

# What the fake's segmented path serves: session/start's infos, one track key
# wrapped under the session key that Secret and the infos give, and per
# format the fragmented MP4 cut into segments and encrypted.
SEGMENTS = FIXTURES / "segments"
SEG_FORMATS = {6: "16-44.1", 7: "24-96", 27: "24-192"}
SEG_SESSION_ID = "qobuzdltest-session"
SEG_INFOS = session_infos("qobuzdltest")
SEG_RAW_KEY = hashlib.sha256(b"qobuzdltest raw key").digest()[:16]
SEG_KEY = wrap_key(derive(FAKE_SECRET, SEG_INFOS), SEG_RAW_KEY, hashlib.md5(b"qobuzdltest key iv").digest())
# Frame sizes and flags cycle; 0x0100 checks that flags is read as 16 bits.
SEG_FRAME_SIZES = (1000, 250, 640, 0, 77)
SEG_FRAME_FLAGS = (1, 0, 0x0100, 1, 0)


def mp4_boxes(data):
    pos, out = 0, []
    while pos < len(data):
        size = int.from_bytes(data[pos:pos + 4], "big")
        out.append((data[pos + 4:pos + 8], data[pos:pos + size]))
        pos += size
    return out


def uuid_box(usertype, data_offset, counter_len, frames):
    """A uuid box as _decrypt_qobuz_segment reads it: 4 bytes it skips, the
    offset of the first frame from the box's start, the counter length, the
    frame count, then per frame its length, 2 bytes it skips, the flags and
    the counter."""
    body = bytes(4) + data_offset.to_bytes(4, "big") + bytes([counter_len]) + len(frames).to_bytes(3, "big")
    for n, flags, counter in frames:
        body += n.to_bytes(4, "big") + b"\x00\x00" + flags.to_bytes(2, "big") + counter
    return (24 + len(body)).to_bytes(4, "big") + b"uuid" + usertype + body


def ctr(key, counter, data):
    e = Cipher(algorithms.AES(key), modes.CTR(counter + bytes(16 - len(counter)))).encryptor()
    return e.update(data) + e.finalize()


def encrypt_fragment(fmt, n, moof, mdat, usertype, counter_len, decoy):
    """Segment n of a format: an optional decoy uuid box, the uuid box, the
    moof and the mdat with the flagged frames encrypted. default_base_moof
    keeps the moof's offsets valid with boxes in front of it."""
    payload = mdat[8:]
    sizes, left, i = [], len(payload), 0
    while left > 0:
        size = min(SEG_FRAME_SIZES[i % len(SEG_FRAME_SIZES)], left)
        sizes.append(size)
        left -= size
        i += 1
    frames = []
    for j, size in enumerate(sizes):
        counter = hashlib.sha256(f"counter {fmt} {n} {j}".encode()).digest()[:counter_len]
        if j == 0 and counter_len == 16:
            # The low 64 bits overflow within the frame, so the carry has to
            # reach the high half.
            counter = counter[:8] + b"\xff" * 7 + b"\xfe"
        frames.append((size, SEG_FRAME_FLAGS[j % len(SEG_FRAME_FLAGS)], counter))
    box_size = 24 + 12 + len(frames) * (8 + counter_len)
    plain_box = uuid_box(usertype, box_size + len(moof) + 8, counter_len, frames)
    out, pos = bytearray(mdat[:8]), 0
    for size, flags, counter in frames:
        chunk = payload[pos:pos + size]
        out += ctr(SEG_RAW_KEY, counter, chunk) if flags else chunk
        pos += size
    head = b""
    if decoy:
        other = hashlib.sha256(b"decoy").digest()[:16]
        head = uuid_box(other, 36 + 8 + 16, 16, [(64, 1, bytes(16))])
    return head + plain_box + moof + bytes(out), head + plain_box + moof + mdat


def write_segment_fixtures():
    shutil.rmtree(SEGMENTS, ignore_errors=True)
    index = {"session_id": SEG_SESSION_ID, "infos": SEG_INFOS, "key": SEG_KEY, "raw_key": SEG_RAW_KEY.hex(), "formats": {}}
    for fmt, name in SEG_FORMATS.items():
        boxes = mp4_boxes((AUDIO / f"{name}.mp4").read_bytes())
        if [t for t, _ in boxes[:2]] != [b"ftyp", b"moov"] or boxes[-1][0] != b"mfra":
            sys.exit(f"{name}.mp4: not a fragmented MP4 as the README makes it")
        fragments = boxes[2:-1]
        usertype = hashlib.sha256(f"segment uuid {fmt}".encode()).digest()[:16]
        counter_len = 16 if fmt == 27 else 8
        d = SEGMENTS / str(fmt)
        d.mkdir(parents=True)
        (d / "0.m4s").write_bytes(boxes[0][1] + boxes[1][1])
        n = 0
        for k in range(0, len(fragments), 2):
            (t1, moof), (t2, mdat) = fragments[k], fragments[k + 1]
            if (t1, t2) != (b"moof", b"mdat"):
                sys.exit(f"{name}.mp4: fragment {k} is {t1} {t2}")
            n += 1
            seg, plain = encrypt_fragment(fmt, n, moof, mdat, usertype, counter_len, decoy=n >= 2 and fmt == 7)
            if downloader._decrypt_qobuz_segment(seg, SEG_RAW_KEY, usertype) != plain:
                sys.exit(f"{name}.mp4: segment {n} does not decrypt back")
            (d / f"{n}.m4s").write_bytes(seg)
        seg1 = (d / "1.m4s").read_bytes()
        if downloader._get_qobuz_segment_uuid(seg1) != usertype:
            sys.exit(f"{name}.mp4: segment 1 gives the wrong UUID")
        index["formats"][str(fmt)] = {"n_segments": n, "mime_type": 'audio/mp4; codecs="flac"'}
    (SEGMENTS / "index.json").write_text(json.dumps(index, indent=1) + "\n", encoding="utf-8")


def box(typ, body):
    return (8 + len(body)).to_bytes(4, "big") + typ + body


def built_segment_cases():
    """Hand-built buffers for _get_qobuz_segment_uuid and
    _decrypt_qobuz_segment: flags, offsets, counters and broken boxes."""
    u = hashlib.sha256(b"built uuid").digest()[:16]
    other = hashlib.sha256(b"other uuid").digest()[:16]
    key = hashlib.sha256(b"built key").digest()[:16]
    data = bytes(range(256)) * 2

    def table(frames, counter_len=8, offset=None, usertype=u, count=None):
        size = 36 + len(frames) * (8 + counter_len)
        b = uuid_box(usertype, size if offset is None else offset, counter_len, frames)
        if count is not None:
            b = b[:32] + b[32:33] + count.to_bytes(3, "big") + b[36:]
        return b

    c8 = lambda s: hashlib.md5(s.encode()).digest()[:8]
    c16 = lambda s: hashlib.md5(s.encode()).digest()
    free = box(b"free", b"x" * 12)
    uuid_cases = {
        "first box": table([(16, 1, c8("a"))]) + data[:16],
        "after other boxes": free + box(b"moof", b"m" * 20) + table([]),
        "first of two": table([], usertype=other) + table([]),
        "none": free + box(b"mdat", data[:40]),
        "empty": b"",
        "zero size stops": bytes(4) + b"free" + bytes(16) + table([]),
        "size past the end stops": (1000).to_bytes(4, "big") + b"free" + bytes(16) + table([]),
        "short box at the end": (24).to_bytes(4, "big") + b"uuid" + u[:12],
        "uuid box shorter than 24": (12).to_bytes(4, "big") + b"uuid" + u[:4] + u[4:] + bytes(12),
        "small size steps": (4).to_bytes(4, "big") + b"uuid" + u + bytes(4),
    }
    for name, buf in uuid_cases.items():
        yield f"uuid: {name}", {"data": buf.hex()}, lambda buf=buf: (lambda r: None if r is None else r.hex())(downloader._get_qobuz_segment_uuid(buf))

    def dec(name, buf, uuid=u, k=key):
        inp = {"data": buf.hex(), "raw_key": k.hex(), "uuid": None if uuid is None else uuid.hex()}
        return f"decrypt: {name}", inp, lambda: downloader._decrypt_qobuz_segment(bytearray(buf), k, uuid).hex()

    three = [(32, 1, c8("1")), (16, 0, c8("2")), (40, 0x0100, c8("3"))]
    yield dec("flags", table(three) + data[:88])
    yield dec("zero flags only", table([(32, 0, c8("1")), (32, 0, c8("2"))]) + data[:64])
    yield dec("frames past the data", table(three) + data[:50])
    yield dec("frame inside the box", table([(20, 1, c8("1")), (8, 1, c8("2"))], offset=8) + data[:8])
    yield dec("frame over its own table", table([(40, 1, c8("1")), (16, 1, c8("2"))], offset=28) + data[:32])
    yield dec("frame after other boxes", table([(24, 1, c8("1"))], offset=36 + 16 + len(free)) + free + data[:24])
    yield dec("counter of 16 with a carry", table([(80, 1, b"\x01" * 8 + b"\xff" * 8)], counter_len=16) + data[:80])
    yield dec("counter of 16 wrapping", table([(48, 1, b"\xff" * 16)], counter_len=16) + data[:48])
    yield dec("counter of 0", table([(32, 1, b"")], counter_len=0) + data[:32])
    yield dec("counter of 17", table([(32, 1, b"c" * 17)], counter_len=17) + data[:32])
    yield dec("counter of 17 unflagged", table([(32, 0, b"c" * 17)], counter_len=17) + data[:32])
    cut = (44).to_bytes(4, "big") + b"uuid" + u + bytes(4) + (100).to_bytes(4, "big") + b"\x08" + (1).to_bytes(3, "big")
    yield dec("counter cut by the end", cut + (16).to_bytes(4, "big") + b"\x00\x00" + (1).to_bytes(2, "big"))
    yield dec("frame count past the table", table([(16, 1, c8("1"))], count=1000) + data[:16])
    yield dec("two boxes", table([(16, 1, c8("1"))]) + data[:16] + table([(16, 1, c8("2"))]) + data[16:32])
    yield dec("other uuid untouched", table([(16, 1, c8("1"))], usertype=other) + data[:16])
    yield dec("uuid None", table([(16, 1, c8("1"))]) + data[:16], uuid=None)
    yield dec("uuid shorter than 16", table([(16, 1, c8("1"))]) + data[:16], uuid=u[:8])
    yield dec("AES-256 key", table([(16, 1, c8("1"))]) + data[:16], k=key * 2)
    yield dec("key of 10 bytes", table([(16, 1, c8("1"))]) + data[:16], k=key[:10])
    yield dec("key of 10 bytes, nothing flagged", table([(16, 0, c8("1"))]) + data[:16], k=key[:10])
    yield dec("zero size stops", bytes(4) + b"free" + table([(16, 1, c8("1"))]) + data[:16])
    yield dec("truncated box stops", table([(16, 1, c8("1"))])[:-1])
    yield dec("size past the end stops", free + (500).to_bytes(4, "big") + b"uuid" + u + data[:40])
    yield dec("header cut short", data[:7])
    yield dec("small size steps", (4).to_bytes(4, "big") + table([(16, 1, c8("1"))]) + data[:16])
    yield dec("uuid box of 24 at the end", (24).to_bytes(4, "big") + b"uuid" + u)
    yield dec("uuid box of 30 at the end", (30).to_bytes(4, "big") + b"uuid" + u + bytes(6))
    yield dec("uuid box of 33 at the end", (33).to_bytes(4, "big") + b"uuid" + u + bytes(4) + bytes(4) + b"\x08")
    yield dec("empty", b"")
    yield dec("table with a 16-byte counter", table([(16, 1, c16("x")), (16, 0, c16("y"))], counter_len=16) + data[:32])


def gen_segments():
    g = Golden("segments")
    index = json.loads((SEGMENTS / "index.json").read_text(encoding="utf-8"))
    raw_key = bytes.fromhex(index["raw_key"])
    for fmt in SEG_FORMATS:
        n = index["formats"][str(fmt)]["n_segments"]
        uuid = downloader._get_qobuz_segment_uuid((SEGMENTS / str(fmt) / "1.m4s").read_bytes())
        for i in range(n + 1):
            seg = (SEGMENTS / str(fmt) / f"{i}.m4s").read_bytes()
            # The flow decrypts segment 0 before the UUID is known.
            use = None if i == 0 else uuid

            def run(seg=seg, use=use):
                found = downloader._get_qobuz_segment_uuid(seg)
                plain = downloader._decrypt_qobuz_segment(seg, raw_key, use)
                return {"uuid": None if found is None else found.hex(), "size": len(plain), "sha256": sha(plain)}

            g.case(f"fixture: {fmt}/{i}", {"format": fmt, "segment": i, "raw_key": raw_key.hex(),
                                             "uuid": None if use is None else use.hex()}, run)
    for name, inp, fn in built_segment_cases():
        g.case(name, inp, fn)
    g.write()


# --- qobuzdltest ---------------------------------------------------------


FAKE_DATA = ROOT / "qobuzdltest" / "testdata"
FAKE_AUDIO = ["16-44.1.flac", "24-96.flac", "24-192.flac", "320.mp3", "cover.jpg", "booklet.pdf"]


def write_fake_fixtures():
    """Copies the fixtures the fake Qobuz serves next to it, so that the
    package embeds them and works from another module's cache."""
    shutil.rmtree(FAKE_DATA, ignore_errors=True)
    shutil.copytree(FIXTURES / "api", FAKE_DATA / "api")
    shutil.copytree(FIXTURES / "bundle", FAKE_DATA / "bundle")
    shutil.copytree(SEGMENTS, FAKE_DATA / "segments")
    (FAKE_DATA / "audio").mkdir()
    for name in FAKE_AUDIO:
        shutil.copyfile(AUDIO / name, FAKE_DATA / "audio" / name)


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
    write_tag_fixtures()
    gen_tags(index, raw)
    gen_metadata_helpers()
    gen_release_types(index, raw)
    gen_disc_track_total(index, raw)
    gen_flac_tagged(index, raw)
    gen_mp3_tagged(index, raw)
    gen_naming(index, raw)
    gen_tracklist(index, raw)
    gen_layout(index, raw)
    gen_core_layout(index, raw)
    write_m3u_fixtures()
    gen_m3u()
    gen_search_text(raw)
    write_db_fixtures()
    gen_db()
    write_segment_fixtures()
    gen_segments()
    write_fake_fixtures()


if __name__ == "__main__":
    main()
