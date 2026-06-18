"""Spider lifecycle tests."""

from __future__ import annotations

from crawlerkit.browser import MockDriver
from crawlerkit.spider import Spider


def test_spider_default_driver_is_mock_when_forced(monkeypatch):
    """``CRAWLERKIT_DRIVER=mock`` makes ``Spider.driver()`` yield a
    :class:`MockDriver` even on an environment with selenium installed."""
    monkeypatch.setenv("CRAWLERKIT_DRIVER", "mock")

    class P(Spider):
        seen: object = None

        def run(self) -> None:
            with self.driver() as d:
                d.get("https://example.com/x")
                P.seen = d

    s = P()
    s.run()
    assert isinstance(P.seen, MockDriver)
    assert P.seen.history == ["https://example.com/x"]


def test_spider_driver_override(monkeypatch):
    """Authors can swap the driver entirely by overriding ``_make_driver``."""

    class StubDriver:
        quit_called = False

        def quit(self):
            type(self).quit_called = True

    class P(Spider):
        def _make_driver(self, *, stealth, headless, **_kw):
            return StubDriver()

        def run(self) -> None:
            with self.driver():
                pass

    P().run()
    assert StubDriver.quit_called is True
