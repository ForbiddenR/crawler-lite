"""Browser driver tests.

Real Selenium isn't exercised here — those tests need Chromium and a
chromedriver, which we don't assume is on the CI image yet. We *do* verify
the factory honours ``CRAWLERKIT_DRIVER`` and that the mock surface stays
stable, which is what protects spiders authored against the documented API.
"""

from __future__ import annotations

import os

import pytest

from crawlerkit.browser import MockDriver, make_driver


def test_mock_driver_records_navigation():
    d = MockDriver()
    assert d.current_url == "about:blank"
    d.get("https://example.com/a")
    d.get("https://example.com/b")
    assert d.history == ["https://example.com/a", "https://example.com/b"]
    assert d.current_url == "https://example.com/b"
    assert d.title.startswith("Mock page:")


def test_mock_screenshot_returns_valid_png():
    d = MockDriver()
    png = d.get_screenshot_as_png()
    assert png[:8] == b"\x89PNG\r\n\x1a\n"
    # IHDR chunk says 1x1 — useful sanity for the screenshot pipeline.
    assert png[16:20] == b"\x00\x00\x00\x01"
    assert png[20:24] == b"\x00\x00\x00\x01"


def test_make_driver_forces_mock(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("CRAWLERKIT_DRIVER", "mock")
    d = make_driver()
    assert isinstance(d, MockDriver)


def test_make_driver_selenium_missing_raises(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("CRAWLERKIT_DRIVER", "selenium")
    # Force "import selenium" to fail by hiding it from sys.modules + path.
    monkeypatch.setattr(
        "crawlerkit.browser._selenium_importable", lambda: False, raising=True
    )
    with pytest.raises(RuntimeError, match="selenium"):
        make_driver()


def test_make_driver_auto_falls_back_to_mock(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.delenv("CRAWLERKIT_DRIVER", raising=False)
    monkeypatch.setattr(
        "crawlerkit.browser._selenium_importable", lambda: False, raising=True
    )
    d = make_driver()
    assert isinstance(d, MockDriver)


def test_make_driver_auto_uses_selenium_when_available(monkeypatch: pytest.MonkeyPatch):
    """When selenium *is* importable we must hit SeleniumDriver — no
    silent mock fallback. Stub the constructor so the test doesn't need a
    real Chrome install."""
    monkeypatch.delenv("CRAWLERKIT_DRIVER", raising=False)
    monkeypatch.setattr(
        "crawlerkit.browser._selenium_importable", lambda: True, raising=True
    )
    seen: dict[str, object] = {}

    class _StubSelenium:
        def __init__(self, **kwargs):
            seen.update(kwargs)

        def quit(self):
            pass

    monkeypatch.setattr("crawlerkit.browser.SeleniumDriver", _StubSelenium)
    d = make_driver(headless=True, stealth=False)
    assert isinstance(d, _StubSelenium)
    assert seen.get("stealth") is False
    assert seen.get("headless") is True


def test_make_driver_picks_up_proxy_env(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("CRAWLERKIT_DRIVER", "selenium")
    monkeypatch.setenv("CRAWLERKIT_PROXY_URL", "http://proxy:3128")
    monkeypatch.setattr(
        "crawlerkit.browser._selenium_importable", lambda: True, raising=True
    )
    seen: dict[str, object] = {}

    class _StubSelenium:
        def __init__(self, **kwargs):
            seen.update(kwargs)

        def quit(self):
            pass

    monkeypatch.setattr("crawlerkit.browser.SeleniumDriver", _StubSelenium)
    make_driver()
    assert seen.get("proxy") == "http://proxy:3128"


def test_make_driver_explicit_proxy_beats_env(monkeypatch: pytest.MonkeyPatch):
    monkeypatch.setenv("CRAWLERKIT_DRIVER", "selenium")
    monkeypatch.setenv("CRAWLERKIT_PROXY_URL", "http://env-proxy:3128")
    monkeypatch.setattr(
        "crawlerkit.browser._selenium_importable", lambda: True, raising=True
    )
    seen: dict[str, object] = {}

    class _StubSelenium:
        def __init__(self, **kwargs):
            seen.update(kwargs)

        def quit(self):
            pass

    monkeypatch.setattr("crawlerkit.browser.SeleniumDriver", _StubSelenium)
    make_driver(proxy="http://kwarg-proxy:9000")
    assert seen.get("proxy") == "http://kwarg-proxy:9000"
