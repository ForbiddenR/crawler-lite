"""Runner harness.

The Go worker invokes this module as `python -m crawlerkit.runner`. We:

  1. Parse env-injected config + args.
  2. Import the spider class named by `entry_module` ("module.path:ClassName").
  3. Construct it with config + args, run setup() → run() → teardown().
  4. On uncaught exception: log it as ERROR (with traceback), exit 1 so the
     worker classifies the task as FAILED. (Captcha is signalled separately
     via the `captcha` event; uncaught CaptchaError still becomes FAILED.)
"""

from __future__ import annotations

import importlib
import json
import os
import sys
import traceback

from . import _events


def _load_spider_class(entry: str):
    """`entry` is "module.path:ClassName" — we split, import, getattr."""
    if ":" not in entry:
        raise ValueError(f"entry_module must look like 'pkg.mod:Class', got {entry!r}")
    mod_path, cls_name = entry.split(":", 1)
    mod = importlib.import_module(mod_path)
    cls = getattr(mod, cls_name, None)
    if cls is None:
        raise AttributeError(f"{mod_path} has no attribute {cls_name!r}")
    return cls


def _read_env_json(name: str) -> dict:
    raw = os.environ.get(name, "")
    if not raw:
        return {}
    try:
        return json.loads(raw)
    except json.JSONDecodeError as e:
        raise ValueError(f"{name} is not valid JSON: {e}") from None


def main() -> int:
    cfg_envelope = _read_env_json("CRAWLERKIT_CONFIG")
    args = _read_env_json("CRAWLERKIT_ARGS")

    entry = cfg_envelope.get("entry_module", "")
    if not entry:
        _events.log("ERROR", "entry_module missing from CRAWLERKIT_CONFIG")
        return 2

    # Make the working directory importable so "spiders.amazon:PriceSpider"
    # resolves against the unzipped source root.
    sys.path.insert(0, os.getcwd())

    try:
        cls = _load_spider_class(entry)
    except Exception as e:  # noqa: BLE001
        _events.log(
            "ERROR",
            f"failed to import spider {entry!r}: {e}",
            traceback=traceback.format_exc(),
        )
        return 1

    spider = cls(config=cfg_envelope.get("config", {}), args=args)

    try:
        spider.setup()
        try:
            spider.run()
        finally:
            spider.teardown()
    except KeyboardInterrupt:
        _events.log("WARN", "spider interrupted")
        return 130
    except Exception as e:  # noqa: BLE001
        _events.log(
            "ERROR",
            f"{type(e).__name__}: {e}",
            traceback=traceback.format_exc(),
        )
        return 1

    return 0


if __name__ == "__main__":
    sys.exit(main())
