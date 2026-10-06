#!/usr/bin/env python3
"""Request older history for chats until a target date is reached.

Usage: python3 backfill.py <target-date YYYY-MM-DD> <chat_jid> [<chat_jid> ...]

Run while the bridge is connected. WhatsApp sends each response asynchronously;
the script waits up to a minute for new rows before deciding that a chat stalled.
"""
import json
import sqlite3
import sys
import time
import urllib.request
from pathlib import Path

DB = Path(__file__).resolve().parent / "store" / "messages.db"
BRIDGE = "http://127.0.0.1:8080"
COUNT = 50
WAIT_SECONDS = 60
POLL_SECONDS = 2


def oldest(chat):
    with sqlite3.connect(f"file:{DB}?mode=ro", uri=True) as db:
        return db.execute(
            "SELECT min(timestamp), count(*) FROM messages WHERE chat_jid = ? AND id != ''", (chat,)
        ).fetchone()


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(1)
    target, chats = sys.argv[1], sys.argv[2:]
    for chat in chats:
        while True:
            before, n_before = oldest(chat)
            if not before:
                print(f"{chat}: no stored message to use as a history anchor")
                break
            if before[:10] <= target:
                print(f"{chat}: reached {before[:10]} ({n_before} messages)")
                break

            body = json.dumps({"chat": chat, "count": COUNT}).encode()
            request = urllib.request.Request(
                BRIDGE + "/api/backfill",
                data=body,
                headers={"Content-Type": "application/json"},
                method="POST",
            )
            try:
                with urllib.request.urlopen(request, timeout=90) as response:
                    json.loads(response.read())
            except Exception as exc:
                print(f"{chat}: request failed: {exc}")
                break

            deadline = time.monotonic() + WAIT_SECONDS
            after, n_after = before, n_before
            progressed = False
            while time.monotonic() < deadline:
                time.sleep(POLL_SECONDS)
                after, n_after = oldest(chat)
                if n_after > n_before or (after and after < before):
                    progressed = True
                    break
            after_label = after[:19] if after else "(none)"
            print(f"{chat}: oldest {before[:19]} -> {after_label} (+{n_after - n_before} messages)", flush=True)
            if not progressed:
                print(f"{chat}: no new history arrived within {WAIT_SECONDS} seconds")
                break


if __name__ == "__main__":
    main()
