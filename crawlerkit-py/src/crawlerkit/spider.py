"""Spider base class.

Authors subclass `Spider` and override `run()`. The runner harness
constructs the instance with the task's config and args injected, calls
`run()`, and turns any uncaught exception into a structured terminal event
before exiting non-zero.
"""

from __future__ import annotations

from contextlib import contextmanager
from typing import Any

from . import _events
from .browser import MockDriver


class Spider:
    """Base class for crawler-lite spiders.

    Override `run()` (required). Optionally override `setup()` and
    `teardown()` for one-off init / cleanup; both are no-ops by default.

    Class attributes:

        rate_limit: free-form string the platform parses. Week 2 ignores it;
            week 3 wires it into the Redis token bucket.

    Instance attributes (populated by the runner):

        config: dict of spider_config["config"] — user-controlled JSON.
        args:   dict of run-now args (or {}).
    """

    rate_limit: str | None = None

    def __init__(self, config: dict[str, Any] | None = None, args: dict[str, Any] | None = None):
        self.config = config or {}
        self.args = args or {}

    # ---- lifecycle hooks ---------------------------------------------------

    def setup(self) -> None:
        """Called once before run(). No-op by default."""
        pass

    def teardown(self) -> None:
        """Called once after run() returns or raises. No-op by default."""
        pass

    def run(self) -> None:
        """Override this. The only required method.

        Raise an exception to mark the task failed. Use crawlerkit.captcha()
        to short-circuit to `captcha_blocked` instead.
        """
        raise NotImplementedError("override Spider.run()")

    # ---- convenience helpers ----------------------------------------------

    @contextmanager
    def driver(self, *, stealth: bool = True, headless: bool = True):
        """Yield a browser-like driver.

        Week 2: MockDriver (records get/find calls; returns None / empty).
        Week 3: real Selenium with selenium-wire + undetected-chromedriver.

        The boolean flags are forwarded so spiders authored against the mock
        will work unchanged once the real driver lands.
        """
        d = self._make_driver(stealth=stealth, headless=headless)
        try:
            yield d
        finally:
            d.quit()

    def _make_driver(self, *, stealth: bool, headless: bool):
        """Driver factory. Override in tests; week 3 swaps in real Selenium."""
        return MockDriver(stealth=stealth, headless=headless)

    # ---- emit shortcuts ---------------------------------------------------

    def log(self, level: str, message: str, **fields: Any) -> None:
        _events.log(level, message, **fields)

    def item(self, payload: Any) -> None:
        _events.item(payload)

    def screenshot(self, name: str, png_bytes: bytes, url: str = "") -> None:
        _events.screenshot(name, png_bytes, url=url)

    def captcha(self, message: str = "") -> None:
        _events.captcha(message)
