"""Downloads one Qobuz URL with qobuz-dl 2.5.9 the way Music Shelf's bot does.

    QOBUZ_TOKEN=... testdata/gen/.venv/bin/python -I testdata/gen/parity.py [--db FILE] URL OUTDIR
    testdata/gen/.venv/bin/python -I testdata/gen/parity.py --fake BASE URL OUTDIR

It builds QobuzDL as the bot does: quality 27, quality fallback on, no
downloads database, the token as the settings' user_auth_token. Then it runs
get_tokens, initialize_client and handle_url. That is the production path,
not the Python CLI's, which reads config.ini. --db FILE gives it a
downloads database instead, for the run that checks Go skips what Python
recorded.

--fake BASE sends every request for https://www.qobuz.com/api.json/0.2/ and
https://play.qobuz.com to BASE, a running qobuzdltest server, and makes
time.sleep return at once. TestParityAgainstFake in cmd/qobuz-dl uses it, so
this script is tested without Qobuz.

The token is read from QOBUZ_TOKEN only and is never printed.
"""

import argparse
import logging
import os
import sys
import time

from requests.adapters import HTTPAdapter

from qobuz_dl.core import QobuzDL
from qobuz_dl.settings import QobuzDLSettings

API_BASE = "https://www.qobuz.com/api.json/0.2/"
PLAY_BASE = "https://play.qobuz.com"


def redirect_to(base):
    """Points the API and the web player at base. CDN and static URLs
    already point at the fake, which rewrites them in its answers."""
    base = base.rstrip("/")
    targets = [(API_BASE, base + "/api.json/0.2/"), (PLAY_BASE + "/", base + "/")]
    send = HTTPAdapter.send

    def send_redirected(self, request, **kwargs):
        for old, new in targets:
            if request.url.startswith(old):
                request.url = new + request.url[len(old):]
                break
        return send(self, request, **kwargs)

    HTTPAdapter.send = send_redirected
    # The downloader pauses a second before each track and between retries.
    time.sleep = lambda seconds: None


def main(argv):
    parser = argparse.ArgumentParser(description="Download one URL as the bot does.")
    parser.add_argument("--fake", metavar="BASE", help="a qobuzdltest server to use instead of Qobuz")
    parser.add_argument("--db", metavar="FILE", help="a downloads database; the bot runs without one")
    parser.add_argument("url")
    parser.add_argument("outdir")
    args = parser.parse_args(argv)

    token = os.environ.get("QOBUZ_TOKEN", "")
    if not token:
        print("parity.py: QOBUZ_TOKEN is not set", file=sys.stderr)
        return 2
    if args.fake:
        redirect_to(args.fake)

    # cli.py's logging setup, so the lines read as the Python CLI's.
    logging.basicConfig(level=logging.INFO, format="%(message)s")

    qobuz = QobuzDL(
        args.outdir,
        quality=27,
        quality_fallback=True,
        downloads_db=args.db,
        settings=QobuzDLSettings(user_auth_token=token),
    )
    qobuz.get_tokens()
    qobuz.initialize_client("", token, qobuz.app_id, qobuz.secrets)
    qobuz.handle_url(args.url)
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
