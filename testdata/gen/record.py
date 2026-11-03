"""Records the Qobuz API answers the Go tests and the fake Qobuz serve.

    QOBUZ_TOKEN=... testdata/gen/.venv/bin/python -I testdata/gen/record.py [--search]

The answers come through qobuz-dl 2.5.9's own Client, so the requests are the
ones production sends. It saves the raw response bodies, from before
_normalize_json_strings, because the Go client normalizes them itself.

--search prints candidates for each fixture kind and records nothing; the
chosen IDs go into ITEMS below and into docs/status.md under Fixture items.

The repo is public, so record.py cleans the answers: signed CDN URLs become
{{CDN}}/file/<track id>/<format id>, the signed blob of a getFileUrl answer
becomes {{BLOB}}, it never saves user/get, and a file that still contains the
token or an app secret aborts the run.
"""

import argparse
import json
import os
import re
import sys
import time
from pathlib import Path

from qobuz_dl.bundle import Bundle
from qobuz_dl.qopy import Client

ROOT = Path(__file__).resolve().parents[2]
OUT = ROOT / "testdata" / "fixtures" / "api"

# Chosen with --search; the reasons are in docs/status.md under Fixture items.
ITEMS = {
    "albums": {
        "multi_disc": "evz7usmruz35a",
        "hires_96": "k43q1q3kx88fa",
        "cd": "0634904080853",
        "compilation": "zcs2h686vhj4b",
        "with_version": "b2d4k8h8pu6zb",
        "classical": "xf2zfbfjz0w1b",
        "goodies": "el3qez8zpc8bc",
    },
    "track": "20273753",
    "playlist": "2082798",
    "artist": "50782",
    "label": "572190",
}

# Searches recorded for the search tests: (type, query).
SEARCHES = [
    ("albums", "miles davis kind of blue"),
    ("tracks", "so what miles davis"),
    ("artists", "miles davis"),
    ("playlists", "jazz"),
]

FORMATS = (5, 6, 7, 27)
PAGE = 50
THROTTLE = 0.3


def make_client():
    token = os.environ.get("QOBUZ_TOKEN", "")
    if not token:
        sys.exit("QOBUZ_TOKEN is not set")
    bundle = Bundle()
    secrets = [s for s in bundle.get_secrets().values() if s]
    client = Client("", "", bundle.get_app_id(), secrets, user_auth_token=token)
    return client, [token, *secrets]


class Recorder:
    """Calls Client.api_call and keeps the raw body of the response."""

    def __init__(self, client):
        self.client = client
        self.last = None
        client.session.hooks["response"].append(self._hook)

    def _hook(self, response, *args, **kwargs):
        self.last = response

    def call(self, epoint, **kwargs):
        time.sleep(THROTTLE)
        self.last = None
        try:
            self.client.api_call(epoint, **kwargs)
        except Exception:
            if self.last is None:
                raise
        r = self.last
        try:
            body = r.json()
        except ValueError:
            body = r.text
        return r.status_code, body


def search(rec, kind, query, limit=50):
    _, body = rec.call("catalog/search", query=query, type=kind, limit=limit)
    return body.get(kind, {}).get("items", []) if isinstance(body, dict) else []


def print_candidates(rec):
    def album_line(a):
        return (
            f"{a['id']:>16}  {a.get('maximum_bit_depth')}/{a.get('maximum_sampling_rate')}"
            f"  discs={a.get('media_count')}  tracks={a.get('tracks_count')}"
            f"  version={a.get('version')!r}  artist={a.get('artist', {}).get('name')!r}"
            f"  title={a.get('title')!r}"
        )

    queries = {
        "multi-disc 24/192": ["remastered deluxe edition 192", "led zeppelin", "pink floyd", "the beatles"],
        "24/96": ["norah jones", "daft punk"],
        "CD": ["nirvana nevermind", "radiohead"],
        "Various Artists": ["now that's what i call music", "jazz compilation"],
        "version in title": ["remastered", "deluxe edition"],
        "classical": ["beethoven symphony karajan", "mahler symphony"],
    }
    seen = {}
    for label, qs in queries.items():
        print(f"\n== {label}")
        for q in qs:
            for a in search(rec, "albums", q, limit=25):
                seen[a["id"]] = a
                ok = {
                    "multi-disc 24/192": (a.get("media_count") or 1) > 1 and a.get("maximum_sampling_rate") == 192,
                    "24/96": a.get("maximum_bit_depth") == 24 and a.get("maximum_sampling_rate") == 96,
                    "CD": a.get("maximum_bit_depth") == 16,
                    "Various Artists": a.get("artist", {}).get("name") == "Various Artists",
                    "version in title": bool(a.get("version")),
                    "classical": "lassical" in json.dumps(a.get("genre", {})),
                }[label]
                if ok:
                    print(album_line(a))

    print("\n== playlists, 5 to 15 tracks")
    for q in ["jazz", "classical", "rock classics", "hi-res"]:
        for p in search(rec, "playlists", q, limit=50):
            n = p.get("tracks_count") or 0
            if 5 <= n <= 15:
                print(f"{p['id']:>12}  tracks={n}  {p.get('name')!r}")

    print("\n== artists")
    for a in search(rec, "artists", "trio", limit=25):
        print(f"{a['id']:>12}  albums={a.get('albums_count')}  {a.get('name')!r}")


def name(epoint, *parts):
    return "-".join([epoint.replace("/", "-"), *[str(p) for p in parts]]) + ".json"


class Store:
    def __init__(self, forbidden):
        self.forbidden = [s for s in forbidden if s]
        self.index = []

    def save(self, fname, epoint, params, status, body):
        text = json.dumps(body, ensure_ascii=False, indent=1) + "\n"
        for s in self.forbidden:
            if s in text:
                sys.exit(f"{fname}: a secret is in the answer; nothing more is written")
        (OUT / fname).write_text(text, encoding="utf-8")
        self.index.append({"endpoint": epoint, "params": params, "status": status, "file": fname})


def clean_file_url(body, track_id, fmt_id):
    if isinstance(body, dict):
        for key in ("url", "url_template"):
            if key in body:
                body[key] = f"{{{{CDN}}}}/file/{track_id}/{fmt_id}"
        if "blob" in body:
            body["blob"] = "{{BLOB}}"
    return body


def record_pages(rec, store, epoint, item_id, items_key):
    """Every page as multi_meta asks for them."""
    offset = 0
    pages = []
    while True:
        status, body = rec.call(epoint, id=item_id, offset=offset, limit=PAGE)
        store.save(name(epoint, item_id, offset), epoint,
                   {"id": item_id, "offset": offset, "limit": PAGE}, status, body)
        if status != 200:
            break
        pages.append(body)
        items = body.get(items_key, {}).get("items", [])
        if not items:
            break
        offset += len(items)
        total = body.get(items_key, {}).get("total", body.get(f"{items_key}_count", 0))
        if offset >= total:
            break
    return pages


def record(rec, store):
    track_ids = []

    for album_id in ITEMS["albums"].values():
        status, body = rec.call("album/get", id=album_id)
        store.save(name("album/get", album_id), "album/get", {"id": album_id}, status, body)
        track_ids += [t["id"] for t in body["tracks"]["items"]]

    tid = ITEMS["track"]
    status, body = rec.call("track/get", id=tid)
    store.save(name("track/get", tid), "track/get", {"id": tid}, status, body)
    track_ids.append(int(tid))

    for page in record_pages(rec, store, "playlist/get", ITEMS["playlist"], "tracks"):
        track_ids += [t["id"] for t in page["tracks"]["items"]]
    record_pages(rec, store, "artist/get", ITEMS["artist"], "albums")
    record_pages(rec, store, "label/get", ITEMS["label"], "albums")

    for kind, query in SEARCHES:
        status, body = rec.call("catalog/search", query=query, type=kind, limit=10)
        store.save(name("catalog/search", kind), "catalog/search",
                   {"query": query, "type": kind, "limit": 10}, status, body)

    for track_id in dict.fromkeys(track_ids):
        for fmt_id in FORMATS:
            status, body = rec.call("track/getFileUrl", id=track_id, fmt_id=fmt_id)
            store.save(name("track/getFileUrl", track_id, fmt_id), "track/getFileUrl",
                       {"id": str(track_id), "format_id": fmt_id}, status,
                       clean_file_url(body, track_id, fmt_id))


def main():
    p = argparse.ArgumentParser()
    p.add_argument("--search", action="store_true")
    args = p.parse_args()

    client, forbidden = make_client()
    rec = Recorder(client)
    if args.search:
        print_candidates(rec)
        return

    if not ITEMS["albums"]:
        sys.exit("ITEMS is empty: pick the IDs with --search first")
    OUT.mkdir(parents=True, exist_ok=True)
    for old in OUT.glob("*.json"):
        old.unlink()
    store = Store(forbidden)
    record(rec, store)
    (OUT / "index.json").write_text(
        json.dumps({"items": ITEMS, "requests": store.index}, ensure_ascii=False, indent=1) + "\n",
        encoding="utf-8",
    )
    print(f"{len(store.index)} answers in {OUT.relative_to(ROOT)}")


if __name__ == "__main__":
    main()
