"""Tests compare.py on trees built from the audio fixtures.

    testdata/gen/.venv/bin/python -I testdata/gen/compare_test.py

-I keeps this folder out of the import path, so compare.py is loaded by its
file name.
"""

import importlib.util
import io
import shutil
import tempfile
import unittest
from pathlib import Path

from mutagen.flac import FLAC, Picture
from mutagen.id3 import ID3, TIT2, TPE1, TXXX
from mutagen.mp3 import MP3

HERE = Path(__file__).resolve().parent
AUDIO = HERE.parent / "fixtures" / "audio"

spec = importlib.util.spec_from_file_location("compare", HERE / "compare.py")
compare = importlib.util.module_from_spec(spec)
spec.loader.exec_module(compare)

ALBUM = "Artist - Album (2020) [24B-96.0kHz]"
UPSTREAM_ROW = "`MakeM3U` reads file names sorted"


class CompareTest(unittest.TestCase):
    def setUp(self):
        self.tmp = Path(tempfile.mkdtemp())
        self.addCleanup(shutil.rmtree, self.tmp)
        self.py = self.tmp / "py"
        self.go = self.tmp / "go"
        self.build(self.py)
        shutil.copytree(self.py, self.go)

    def build(self, root):
        album = root / ALBUM
        (album / "CD 01").mkdir(parents=True)
        shutil.copy(AUDIO / "24-96.flac", album / "CD 01" / "01 - One.flac")
        f = FLAC(album / "CD 01" / "01 - One.flac")
        f["TITLE"] = "One"
        f["ARTIST"] = ["A", "B"]
        f["TRACKNUMBER"] = "01"
        f.save()
        shutil.copy(AUDIO / "320-bare.mp3", album / "02 - Two.mp3")
        tags = ID3()
        tags.add(TIT2(encoding=3, text="Two"))
        tags.add(TPE1(encoding=3, text="A"))
        tags.add(TXXX(encoding=3, desc="QOBUZTRACKID", text="2"))
        tags.save(album / "02 - Two.mp3", v2_version=3)
        shutil.copy(AUDIO / "cover.jpg", album / "cover.jpg")
        (album / "tracklist.txt").write_text("1. One\n2. Two\n", encoding="utf-8")
        (root / "Playlist.m3u8").write_text("#EXTM3U\n", encoding="utf-8")

    def diffs(self):
        return [(d.kind, d.path, d.key) for d in compare.compare(self.py, self.go)]

    def run_main(self, *args):
        out = io.StringIO()
        code = compare.main([*args, str(self.py), str(self.go)], out=out)
        return code, out.getvalue()

    def allow_file(self, text):
        p = self.tmp / "allow.txt"
        p.write_text(text, encoding="utf-8")
        return str(p)

    def test_identical_trees_pass(self):
        self.assertEqual(self.diffs(), [])
        code, out = self.run_main()
        self.assertEqual(code, 0, out)
        self.assertIn("0 differences, 0 not allowed", out)

    def test_renamed_file(self):
        flac = self.go / ALBUM / "CD 01" / "01 - One.flac"
        flac.rename(flac.with_name("01 - Uno.flac"))
        self.assertEqual(self.diffs(), [
            ("only-py", f"{ALBUM}/CD 01/01 - One.flac", ""),
            ("only-go", f"{ALBUM}/CD 01/01 - Uno.flac", ""),
        ])
        self.assertEqual(self.run_main()[0], 1)

    def test_extra_file_and_folder(self):
        (self.go / ALBUM / "Digital Booklet.txt").write_text("x", encoding="utf-8")
        (self.py / "[INCOMPLETE] Other").mkdir()
        self.assertEqual(self.diffs(), [
            ("only-go", f"{ALBUM}/Digital Booklet.txt", ""),
            ("only-py", "[INCOMPLETE] Other", ""),
        ])

    def test_changed_flac_tag(self):
        f = FLAC(self.go / ALBUM / "CD 01" / "01 - One.flac")
        f["TITLE"] = "Uno"
        f.save()
        self.assertEqual(self.diffs(), [("tags", f"{ALBUM}/CD 01/01 - One.flac", "TITLE")])
        code, out = self.run_main()
        self.assertEqual(code, 1)
        self.assertIn("py ['TITLE=One'] go ['TITLE=Uno']", out)

    def test_flac_comment_order(self):
        f = FLAC(self.go / ALBUM / "CD 01" / "01 - One.flac")
        comments = list(f.tags)
        f.tags.clear()
        for k, v in comments[1:] + comments[:1]:
            f.tags.append((k, v))
        f.save()
        self.assertEqual(self.diffs(), [("tags", f"{ALBUM}/CD 01/01 - One.flac", "(order)")])

    def test_flac_picture(self):
        f = FLAC(self.go / ALBUM / "CD 01" / "01 - One.flac")
        p = Picture()
        p.type, p.mime, p.data = 3, "image/jpeg", (AUDIO / "cover.jpg").read_bytes()
        f.add_picture(p)
        f.save()
        self.assertEqual(self.diffs(), [("tags", f"{ALBUM}/CD 01/01 - One.flac", "(pictures)")])

    def test_changed_mp3_frame(self):
        path = self.go / ALBUM / "02 - Two.mp3"
        tags = ID3(path)
        tags.add(TPE1(encoding=3, text="B"))
        tags.save(path, v2_version=3)
        self.assertEqual(self.diffs(), [("tags", f"{ALBUM}/02 - Two.mp3", "TPE1")])

    def test_mp3_frames_are_a_set(self):
        # The same frames saved in another order and with other padding.
        path = self.go / ALBUM / "02 - Two.mp3"
        old = ID3(path)
        frames = list(old.values())
        tags = ID3()
        for frame in reversed(frames):
            tags.add(frame)
        tags.save(path, v2_version=3, padding=lambda info: 4096)
        self.assertEqual(self.diffs(), [])

    def test_mp3_version(self):
        path = self.go / ALBUM / "02 - Two.mp3"
        ID3(path).save(path, v2_version=4)
        self.assertEqual(self.diffs(), [("tags", f"{ALBUM}/02 - Two.mp3", "(version)")])

    def test_stream_info(self):
        shutil.copy(AUDIO / "24-192.flac", self.go / ALBUM / "CD 01" / "01 - One.flac")
        got = self.diffs()
        keys = {k for kind, _, k in got if kind == "stream"}
        self.assertTrue({"sample_rate", "total_samples", "audio-sha256"} <= keys, got)

    def test_audio_frames(self):
        path = self.go / ALBUM / "CD 01" / "01 - One.flac"
        data = bytearray(path.read_bytes())
        data[-1] ^= 0xFF
        path.write_bytes(bytes(data))
        self.assertEqual(self.diffs(), [("stream", f"{ALBUM}/CD 01/01 - One.flac", "audio-sha256")])

    def test_unreadable_audio(self):
        (self.go / ALBUM / "02 - Two.mp3").write_bytes(b"not audio")
        self.assertEqual(self.diffs(), [("unreadable", f"{ALBUM}/02 - Two.mp3", "go")])

    def test_size_and_content(self):
        (self.go / ALBUM / "tracklist.txt").write_text("1. One\n", encoding="utf-8")
        (self.go / "Playlist.m3u8").write_text("#EXTM3V\n", encoding="utf-8")
        self.assertEqual(self.diffs(), [
            ("size", f"{ALBUM}/tracklist.txt", ""),
            ("content", "Playlist.m3u8", ""),
        ])

    def test_allow_list(self):
        (self.go / "Playlist.m3u8").write_text("#EXTM3V\n", encoding="utf-8")
        allow = self.allow_file(
            "# a comment\n\n"
            f"content *.m3u8 -- UPSTREAM.md: {UPSTREAM_ROW}\n"
            f"tags '{ALBUM}/*' TITLE -- UPSTREAM.md: {UPSTREAM_ROW}\n"
        )
        code, out = self.run_main("--allow", allow)
        self.assertEqual(code, 0, out)
        self.assertIn("allowed content Playlist.m3u8", out)
        self.assertIn("unused allow line: tags", out)

        # The key pattern limits the line to that key.
        f = FLAC(self.go / ALBUM / "CD 01" / "01 - One.flac")
        f["ARTIST"] = "C"
        f.save()
        code, out = self.run_main("--allow", allow)
        self.assertEqual(code, 1, out)
        self.assertIn("1 not allowed", out)

    def test_allow_list_needs_an_upstream_row(self):
        for text in (
            "content *.m3u8\n",
            "content *.m3u8 -- UPSTREAM.md:\n",
            "content *.m3u8 -- UPSTREAM.md: a row nobody wrote\n",
            f"renamed *.m3u8 -- UPSTREAM.md: {UPSTREAM_ROW}\n",
            f"content -- UPSTREAM.md: {UPSTREAM_ROW}\n",
        ):
            code, _ = self.run_main("--allow", self.allow_file(text))
            self.assertEqual(code, 2, text)

    def test_the_committed_allow_list_loads(self):
        upstream = (HERE.parents[1] / "UPSTREAM.md").read_text(encoding="utf-8")
        compare.load_allow(compare.DEFAULT_ALLOW, upstream)


if __name__ == "__main__":
    unittest.main()
