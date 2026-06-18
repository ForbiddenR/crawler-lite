"""FD3 event protocol — emit structured events from a spider back to the
worker that spawned it.

The worker passes file descriptor 3 to the child process as the write end
of a pipe; the master never sees this — only the local worker reads it. We
write JSON Lines: one JSON object per line, each with shape::

    {"type": "log"|"item"|"shot"|"captcha", "data": {...}}

Stdout and stderr are reserved for user `print()` calls and Python tracebacks
respectively; the worker forwards those as INFO/ERROR log lines so a
`print("hello")` still surfaces in the live tail.
"""

from __future__ import annotations

import json
import os
import sys
import time
from typing import Any

# The worker sets CRAWLERKIT_EVENT_FD=3 when it spawns us. If it's missing
# (e.g. running tests outside the worker) we fall back to stderr so events
# are at least visible.
_FD_VAR = "CRAWLERKIT_EVENT_FD"


def _open_event_stream():
    fd_str = os.environ.get(_FD_VAR)
    if not fd_str:
        # No worker — events go to stderr, prefixed so they're easy to grep.
        return sys.stderr, True
    try:
        fd = int(fd_str)
    except ValueError:
        return sys.stderr, True
    return os.fdopen(fd, "w", buffering=1, encoding="utf-8"), False


_stream, _fallback = _open_event_stream()


def _emit(type_: str, data: dict[str, Any]) -> None:
    line = json.dumps({"type": type_, "data": data}, separators=(",", ":"))
    if _fallback:
        _stream.write(f"[crawlerkit-event] {line}\n")
    else:
        _stream.write(line + "\n")


def log(level: str, message: str, **fields: Any) -> None:
    """Emit a structured log line.

    The worker turns this into a pb.LogLine and the master fans it out over
    Redis pubsub so the UI sees it live.

    `fields` are attached to the message body so spiders can include
    structured context (e.g. `log("WARN", "rate limit", domain="x.com")`).
    """
    payload: dict[str, Any] = {
        "level": level,
        "message": message,
        "ts_ns": time.time_ns(),
    }
    if fields:
        payload["fields"] = fields
    _emit("log", payload)


def item(payload: Any) -> None:
    """Emit a structured item — anything JSON-serializable.

    Items end up in Postgres, queryable by task_id and spider_id.
    """
    # Round-trip through json.dumps to fail fast if the user emits something
    # non-serializable (would otherwise blow up the worker pump goroutine).
    _emit("item", {"payload": json.loads(json.dumps(payload, default=str))})


def screenshot(name: str, png_bytes: bytes, url: str = "", *, key: str | None = None) -> None:
    """Upload a PNG screenshot to MinIO and notify the master.

    `name` is a short label for the timeline (e.g. "listing-page").
    `png_bytes` is the raw image. `url` is the page URL when captured (used
    for "show me the page that produced this shot" UX).

    The worker provided a presigned PUT URL via CRAWLERKIT_PRESIGN_PUT — we
    rewrite it for this specific key. If upload fails we still emit the shot
    event so the UI shows the failure.
    """
    if key is None:
        key = f"tasks/{os.environ.get('CRAWLERKIT_TASK_ID', '0')}/{name}.png"

    width, height = _png_dimensions(png_bytes)
    uploaded = _upload(key, png_bytes, "image/png")
    _emit(
        "shot",
        {
            "name": name,
            "key": key,
            "url": url,
            "bytes": len(png_bytes),
            "width": width,
            "height": height,
            "uploaded": uploaded,
        },
    )


def captcha(message: str = "") -> None:
    """Signal that the spider hit a captcha. The master moves the task to
    `captcha_blocked` status and (in week 3) routes it to the human queue.
    """
    _emit("captcha", {"message": message})


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------


def _upload(key: str, body: bytes, content_type: str) -> bool:
    base = os.environ.get("CRAWLERKIT_PRESIGN_PUT", "")
    if not base:
        return False
    # The worker presigned a generic placeholder key; we replace just the path.
    # In dev MinIO this works because the signature covers query params, not
    # path. For production replace this with an explicit per-event presign
    # round-trip when bandwidth allows.
    try:
        import httpx

        url = _rewrite_presigned_key(base, key)
        r = httpx.put(url, content=body, headers={"Content-Type": content_type}, timeout=30)
        return r.is_success
    except Exception:
        return False


def _rewrite_presigned_key(presign_url: str, new_key: str) -> str:
    # presign_url looks like:  http://host/bucket/tasks/123/_placeholder?...
    # We swap "_placeholder" -> the actual basename, but only if the prefix
    # path matches. For week 2 the dev workflow uses MinIO with path-style
    # URLs so a substring replace is safe.
    placeholder = "_placeholder"
    if placeholder in presign_url:
        # New key example: tasks/123/listing.png
        basename = new_key.rsplit("/", 1)[-1]
        return presign_url.replace(placeholder, basename, 1)
    return presign_url


def _png_dimensions(b: bytes) -> tuple[int, int]:
    """Return (width, height) of a PNG without depending on Pillow.

    PNG header layout: 8-byte signature, then an IHDR chunk:
        4 bytes length | 4 bytes "IHDR" | 4 bytes width | 4 bytes height | ...
    """
    if len(b) < 24 or b[:8] != b"\x89PNG\r\n\x1a\n":
        return 0, 0
    width = int.from_bytes(b[16:20], "big")
    height = int.from_bytes(b[20:24], "big")
    return width, height
