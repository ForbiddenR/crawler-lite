"""Quick fixture spider for load testing.

Sleeps ~1s and emits a single item. Used by loadtest/queue_burst.sh to
measure end-to-end pipeline throughput without coupling the test to any
real site or to Selenium (no browser → no Chromium startup cost → the
numbers reflect master dispatch + gRPC + Python IPC, not rendering).

To use:
  1. Put this file in a git repo (or a local git repo at this path).
  2. Create a spider via the UI/API with:
       entry_module = "spider:QuickSpider"
       git_url      = <url or file:// path to this repo>
  3. Sync the spider, then note its ID and pass it to queue_burst.sh.
"""

from __future__ import annotations

import time

from crawlerkit import Spider


class QuickSpider(Spider):
    def run(self) -> None:
        self.log("INFO", "quick spider starting")
        time.sleep(1)
        self.item({"ok": True, "spider": "quick"})
        self.log("INFO", "quick spider done")
