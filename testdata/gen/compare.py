"""Compares two download trees, Python qobuz-dl's and the Go port's.

    testdata/gen/.venv/bin/python -I testdata/gen/compare.py [--allow FILE] PY_DIR GO_DIR

It walks both trees and prints one line per difference:

    only-py PATH, only-go PATH    a file or folder on one side only
    size PATH                     a non-audio file of another size
    content PATH                  a non-audio file of the same size with other bytes
    tags PATH KEY                 FLAC comments, in order, and pictures; MP3
                                  frames, as a set
    stream PATH FIELD             stream info and a hash of the audio frames
    unreadable PATH               mutagen can't read the file on one side

PATH is relative to the tree's root, with / between folders. Audio files are
the .flac and .mp3 ones; mutagen reads them.

The allow list, testdata/gen/parity-allow.txt unless --allow says otherwise,
holds the differences the port makes on purpose. Each line is

    KIND PATH-PATTERN [KEY-PATTERN] -- UPSTREAM.md: TEXT

split as a shell would, so a pattern with spaces goes in quotes. In a
pattern * matches any run of characters, / included, and ? one character;
nothing else is special, so brackets in folder names need no escaping. TEXT must appear in UPSTREAM.md, so each allowed difference names
the row that explains it. A line with no KEY-PATTERN matches any key.

Exit status: 0 when every difference is allowed, 1 otherwise, 2 for bad
arguments or a bad allow list.
"""

import argparse
import hashlib
import os
import re
import shlex
import sys
from dataclasses import dataclass
from pathlib import Path

from mutagen.flac import FLAC
from mutagen.mp3 import MP3

ROOT = Path(__file__).resolve().parents[2]
DEFAULT_ALLOW = ROOT / "testdata" / "gen" / "parity-allow.txt"
AUDIO = (".flac", ".mp3")
KINDS = ("only-py", "only-go", "size", "content", "tags", "stream", "unreadable")


@dataclass
class Diff:
    kind: str
    path: str
    key: str = ""
    detail: str = ""

    def line(self):
        out = f"{self.kind} {self.path}"
        if self.key:
            out += f" [{self.key}]"
        if self.detail:
            out += f": {self.detail}"
        return out


@dataclass
class Allow:
    kind: str
    path: re.Pattern
    key: re.Pattern | None
    ref: str
    text: str
    used: bool = False

    def matches(self, d):
        if d.kind != self.kind or not self.path.fullmatch(d.path):
            return False
        return self.key is None or bool(self.key.fullmatch(d.key))


def pattern(s):
    """A pattern where * is any run of characters and ? one character."""
    out = "".join(".*" if c == "*" else "." if c == "?" else re.escape(c) for c in s)
    return re.compile(out, re.DOTALL)


def load_allow(path, upstream_text):
    """Reads the allow list; a line it can't read is a ValueError."""
    allows = []
    for n, raw in enumerate(Path(path).read_text(encoding="utf-8").splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        rule, sep, ref = line.partition(" -- UPSTREAM.md:")
        ref = ref.strip()
        if not sep or not ref:
            raise ValueError(f"{path}:{n}: no '-- UPSTREAM.md: <row>' reference")
        if ref not in upstream_text:
            raise ValueError(f"{path}:{n}: UPSTREAM.md has no {ref!r}")
        try:
            fields = shlex.split(rule)
        except ValueError as e:
            raise ValueError(f"{path}:{n}: {e}") from None
        if len(fields) not in (2, 3) or fields[0] not in KINDS:
            raise ValueError(f"{path}:{n}: want KIND PATH-PATTERN [KEY-PATTERN], KIND one of {', '.join(KINDS)}")
        key = pattern(fields[2]) if len(fields) == 3 else None
        allows.append(Allow(fields[0], pattern(fields[1]), key, ref, line))
    return allows


def walk(root):
    """Every file and folder under root: relative path to is-folder."""
    out = {}
    for dirpath, dirnames, filenames in os.walk(root):
        rel = os.path.relpath(dirpath, root)
        for name in dirnames:
            out[Path(rel, name).as_posix() if rel != "." else name] = True
        for name in filenames:
            out[Path(rel, name).as_posix() if rel != "." else name] = False
    return out


def flac_audio_offset(data):
    """Where the first audio frame starts: after any ID3v2 tag, the fLaC
    marker and the metadata blocks."""
    pos = 0
    if data[:3] == b"ID3" and len(data) >= 10:
        size = 0
        for b in data[6:10]:
            size = (size << 7) | (b & 0x7F)
        pos = 10 + size + (10 if data[5] & 0x10 else 0)
    if data[pos:pos + 4] != b"fLaC":
        raise ValueError("no fLaC marker")
    pos += 4
    while True:
        if pos + 4 > len(data):
            raise ValueError("metadata runs past the end")
        header = data[pos]
        pos += 4 + int.from_bytes(data[pos + 1:pos + 4], "big")
        if header & 0x80:
            return pos


def mp3_audio(data, tags):
    """The bytes between the ID3v2 tag and any ID3v1 tag."""
    start = tags.size if tags is not None else 0
    end = len(data) - 128 if data[-128:-125] == b"TAG" else len(data)
    return data[start:end]


def read_flac(path):
    f = FLAC(path)
    data = Path(path).read_bytes()
    tags = {
        "(vendor)": [f.tags.vendor if f.tags is not None else None],
        "(comments)": [f"{k}={v}" for k, v in (f.tags or [])],
        "(pictures)": [
            f"type={p.type} mime={p.mime} desc={p.desc!r} {p.width}x{p.height}x{p.depth} "
            f"sha256={hashlib.sha256(p.data).hexdigest()[:16]}"
            for p in f.pictures
        ],
    }
    i = f.info
    stream = {
        "sample_rate": i.sample_rate,
        "bits_per_sample": i.bits_per_sample,
        "channels": i.channels,
        "total_samples": i.total_samples,
        "md5_signature": f"{i.md5_signature:032x}",
        "audio-sha256": hashlib.sha256(data[flac_audio_offset(data):]).hexdigest(),
    }
    return tags, stream


def read_mp3(path):
    f = MP3(path)
    data = Path(path).read_bytes()
    frames = {}
    if f.tags is not None:
        for frame in f.tags.values():
            frames.setdefault(frame.HashKey, []).append(repr(frame))
        frames["(version)"] = [".".join(map(str, f.tags.version))]
    i = f.info
    stream = {
        "sample_rate": i.sample_rate,
        "bitrate": i.bitrate,
        "channels": i.channels,
        "mode": i.mode,
        "length": round(i.length, 6),
        "audio-sha256": hashlib.sha256(mp3_audio(data, f.tags)).hexdigest(),
    }
    return {k: sorted(v) for k, v in frames.items()}, stream


def comments_by_key(comments):
    """KEY=value comments grouped by key, keys in first-seen order."""
    out = {}
    for c in comments:
        out.setdefault(c.split("=", 1)[0], []).append(c)
    return out


def compare_tags(path, py, go, ordered):
    """FLAC comments compare in order, key by key, then as a whole for the
    order; MP3 frames compare as a set per hash key."""
    diffs = []
    if ordered:
        py_c, go_c = py.pop("(comments)"), go.pop("(comments)")
        pk, gk = comments_by_key(py_c), comments_by_key(go_c)
        for k in sorted(pk.keys() | gk.keys()):
            if pk.get(k) != gk.get(k):
                diffs.append(Diff("tags", path, k, f"py {pk.get(k, [])} go {gk.get(k, [])}"))
        if not diffs and py_c != go_c:
            keys = lambda c: [x.split("=", 1)[0] for x in c]
            diffs.append(Diff("tags", path, "(order)", f"py {keys(py_c)} go {keys(go_c)}"))
    for k in sorted(py.keys() | go.keys()):
        if py.get(k) != go.get(k):
            diffs.append(Diff("tags", path, k, f"py {py.get(k, [])} go {go.get(k, [])}"))
    return diffs


def compare_audio(path, py_file, go_file):
    reader = read_flac if path.lower().endswith(".flac") else read_mp3
    read = {}
    for side, f in (("py", py_file), ("go", go_file)):
        try:
            read[side] = reader(f)
        except Exception as e:  # noqa: BLE001 - any failure to read is a difference
            return [Diff("unreadable", path, side, f"{type(e).__name__}: {e}")]
    (py_tags, py_stream), (go_tags, go_stream) = read["py"], read["go"]
    diffs = compare_tags(path, py_tags, go_tags, reader is read_flac)
    for field in py_stream:
        if py_stream[field] != go_stream[field]:
            diffs.append(Diff("stream", path, field, f"py {py_stream[field]} go {go_stream[field]}"))
    return diffs


def compare(py_root, go_root):
    py, go = walk(py_root), walk(go_root)
    diffs = []
    for path in sorted(py.keys() | go.keys()):
        if path not in go:
            diffs.append(Diff("only-py", path, detail="folder" if py[path] else ""))
            continue
        if path not in py:
            diffs.append(Diff("only-go", path, detail="folder" if go[path] else ""))
            continue
        if py[path] != go[path]:
            diffs.append(Diff("only-py" if py[path] else "only-go", path, detail="a folder here, a file there"))
            continue
        if py[path]:
            continue
        py_file, go_file = os.path.join(py_root, path), os.path.join(go_root, path)
        if path.lower().endswith(AUDIO):
            diffs.extend(compare_audio(path, py_file, go_file))
            continue
        py_size, go_size = os.path.getsize(py_file), os.path.getsize(go_file)
        if py_size != go_size:
            diffs.append(Diff("size", path, detail=f"py {py_size} go {go_size}"))
        elif Path(py_file).read_bytes() != Path(go_file).read_bytes():
            diffs.append(Diff("content", path))
    return diffs


def main(argv, out=sys.stdout):
    parser = argparse.ArgumentParser(description="Compare two download trees.")
    parser.add_argument("--allow", default=str(DEFAULT_ALLOW), help="the allow list")
    parser.add_argument("py_dir")
    parser.add_argument("go_dir")
    args = parser.parse_args(argv)
    for d in (args.py_dir, args.go_dir):
        if not os.path.isdir(d):
            print(f"compare.py: {d} is not a folder", file=sys.stderr)
            return 2
    try:
        upstream = (ROOT / "UPSTREAM.md").read_text(encoding="utf-8")
        allows = load_allow(args.allow, upstream)
    except (OSError, ValueError) as e:
        print(f"compare.py: {e}", file=sys.stderr)
        return 2

    diffs = compare(args.py_dir, args.go_dir)
    failed = 0
    for d in diffs:
        allow = next((a for a in allows if a.matches(d)), None)
        if allow:
            allow.used = True
            print(f"allowed {d.line()} (UPSTREAM.md: {allow.ref})", file=out)
        else:
            failed += 1
            print(d.line(), file=out)
    for a in allows:
        if not a.used:
            print(f"unused allow line: {a.text}", file=out)
    print(f"{len(diffs)} differences, {failed} not allowed", file=out)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
