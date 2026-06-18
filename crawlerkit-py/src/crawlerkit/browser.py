"""Browser drivers for crawlerkit.

Two implementations behind the same surface:

* ``SeleniumDriver`` — real Chromium via Selenium 4. The default when
  ``selenium`` is importable and the runtime hasn't asked for the mock.
* ``MockDriver`` — recorded-call stand-in. Used by tests and as a fallback
  when Selenium isn't installed (so authoring on a laptop without Chromium
  still works against the mock).

Selection is controlled by the ``CRAWLERKIT_DRIVER`` env var:

* ``CRAWLERKIT_DRIVER=mock`` → always mock.
* ``CRAWLERKIT_DRIVER=selenium`` → real driver; raises if selenium is missing.
* unset → real driver if importable, else mock with a warning event.

Spider authors don't usually pick directly — ``Spider.driver()`` calls
``make_driver()`` from this module which honours the env var.

Stealth in this MVP is intentionally light: a few flags and a pre-attached
``navigator.webdriver = undefined`` patch. We do *not* bundle
``undetected-chromedriver`` (heavy, pins old chromedriver versions). Sites
that need stronger evasion can override ``Spider._make_driver`` and pull in
``undetected-chromedriver`` via the optional ``[selenium]`` extra.
"""

from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Any

# 1x1 transparent PNG — useful for testing the screenshot pipeline without
# spawning a real browser.
_TINY_PNG = (
    b"\x89PNG\r\n\x1a\n"
    b"\x00\x00\x00\rIHDR"
    b"\x00\x00\x00\x01\x00\x00\x00\x01"  # 1x1
    b"\x08\x06\x00\x00\x00"
    b"\x1f\x15\xc4\x89"
    b"\x00\x00\x00\rIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4"
    b"\x00\x00\x00\x00IEND\xaeB`\x82"
)


# ---------------------------------------------------------------------------
# Mock — used by tests and as a graceful fallback when Selenium is missing.
# ---------------------------------------------------------------------------


@dataclass
class MockElement:
    """Stub returned by find_element. Returns sensible defaults so spiders
    written against the real driver mostly work without conditionals."""

    tag: str = "div"
    text: str = ""
    attributes: dict[str, str] = field(default_factory=dict)

    def get_attribute(self, name: str) -> str:
        return self.attributes.get(name, "")

    def click(self) -> None:  # pragma: no cover - stub
        pass

    def send_keys(self, *_args: Any) -> None:  # pragma: no cover - stub
        pass

    def find_element(self, by: str, value: str) -> "MockElement":
        return MockElement()

    def find_elements(self, by: str, value: str) -> list["MockElement"]:
        return []


@dataclass
class MockDriver:
    """Selenium-shaped mock. Records visited URLs in ``.history``."""

    stealth: bool = True
    headless: bool = True
    current_url: str = "about:blank"
    title: str = ""
    history: list[str] = field(default_factory=list)

    def get(self, url: str) -> None:
        self.current_url = url
        self.title = f"Mock page: {url}"
        self.history.append(url)

    def find_element(self, by: str, value: str) -> MockElement:
        return MockElement()

    def find_elements(self, by: str, value: str) -> list[MockElement]:
        return []

    def execute_script(self, script: str, *args: Any) -> Any:
        return None

    def get_screenshot_as_png(self) -> bytes:
        return _TINY_PNG

    def set_page_load_timeout(self, _seconds: float) -> None:
        pass

    def set_script_timeout(self, _seconds: float) -> None:
        pass

    def quit(self) -> None:
        pass


# ---------------------------------------------------------------------------
# Real driver — thin wrapper around selenium.webdriver.Chrome.
# ---------------------------------------------------------------------------


def _stealth_script() -> str:
    """JS injected before any page script runs.

    Hides the most obvious automation tells. This is *not* a full bypass —
    serious bot-detection still wins — but it removes the cheap signals so
    benign sites stop tripping their captcha on first page load."""
    return (
        "Object.defineProperty(navigator, 'webdriver', {get: () => undefined});"
        "Object.defineProperty(navigator, 'languages', {get: () => ['en-US','en']});"
        "Object.defineProperty(navigator, 'plugins', {get: () => [1,2,3,4,5]});"
    )


class SeleniumDriver:
    """Wraps ``selenium.webdriver.Chrome`` so it matches the MockDriver API.

    Construction is lazy: importing this module never imports selenium; the
    first instantiation does. This keeps ``import crawlerkit`` cheap for
    tooling that just wants the events surface.

    Most attributes (``current_url``, ``title``, ``find_element*``,
    ``execute_script``, ``get_screenshot_as_png``, ``quit``) are forwarded
    via ``__getattr__`` to the underlying selenium driver.
    """

    def __init__(
        self,
        *,
        stealth: bool = True,
        headless: bool = True,
        proxy: str | None = None,
        user_agent: str | None = None,
        binary: str | None = None,
        page_load_timeout: float = 30.0,
        script_timeout: float = 30.0,
        extra_args: list[str] | None = None,
    ) -> None:
        # Imported here so ``import crawlerkit`` doesn't require selenium.
        from selenium import webdriver
        from selenium.webdriver.chrome.options import Options

        opts = Options()
        if headless:
            # --headless=new is Chromium's modern headless mode (matches
            # rendering of the visible browser); the legacy --headless still
            # diverges on a few sites.
            opts.add_argument("--headless=new")
        # Container-friendly defaults — no-op on a developer laptop.
        opts.add_argument("--no-sandbox")
        opts.add_argument("--disable-dev-shm-usage")
        opts.add_argument("--disable-gpu")
        opts.add_argument("--window-size=1920,1080")
        if user_agent:
            opts.add_argument(f"--user-agent={user_agent}")
        if proxy:
            opts.add_argument(f"--proxy-server={proxy}")
        if binary:
            opts.binary_location = binary
        for arg in extra_args or []:
            opts.add_argument(arg)
        if stealth:
            # Suppress the obvious "automation" infobar / extension switch.
            opts.add_experimental_option("excludeSwitches", ["enable-automation"])
            opts.add_experimental_option("useAutomationExtension", False)

        # Selenium 4.6+ auto-resolves chromedriver via Selenium Manager. No
        # need for webdriver-manager or a vendored binary.
        self._driver = webdriver.Chrome(options=opts)
        self._driver.set_page_load_timeout(page_load_timeout)
        self._driver.set_script_timeout(script_timeout)

        if stealth:
            # Inject the stealth shim *before* page scripts run so detection
            # checks evaluating at document_start see the patched values.
            try:
                self._driver.execute_cdp_cmd(
                    "Page.addScriptToEvaluateOnNewDocument",
                    {"source": _stealth_script()},
                )
            except Exception:
                # Non-Chromium remote drivers don't expose the CDP shortcut;
                # accept the slightly weaker stealth rather than crashing.
                pass

    # ---- API the spider calls --------------------------------------------

    def get(self, url: str) -> None:
        self._driver.get(url)

    def quit(self) -> None:
        try:
            self._driver.quit()
        except Exception:  # pragma: no cover - cleanup best-effort
            pass

    # Forward everything else (find_element, execute_script, screenshots,
    # current_url, title, ...) so the wrapper matches the MockDriver surface
    # without re-listing every method.
    def __getattr__(self, name: str) -> Any:
        return getattr(self._driver, name)


# ---------------------------------------------------------------------------
# Factory
# ---------------------------------------------------------------------------


def _selenium_importable() -> bool:
    try:
        import selenium  # noqa: F401
    except ImportError:
        return False
    return True


def make_driver(*, stealth: bool = True, headless: bool = True, **kwargs: Any):
    """Return a driver honouring ``CRAWLERKIT_DRIVER``.

    * ``mock`` → :class:`MockDriver`.
    * ``selenium`` → :class:`SeleniumDriver`; raises if selenium is missing.
    * unset → :class:`SeleniumDriver` if importable, else :class:`MockDriver`.

    Extra ``**kwargs`` forward to :class:`SeleniumDriver` (proxy, user_agent,
    binary, page_load_timeout, script_timeout, extra_args). They're ignored
    by the mock so spiders authored against the real driver still work in
    tests.
    """
    choice = (os.environ.get("CRAWLERKIT_DRIVER") or "").strip().lower()

    if choice == "mock":
        return MockDriver(stealth=stealth, headless=headless)
    if choice == "selenium":
        if not _selenium_importable():
            raise RuntimeError(
                "CRAWLERKIT_DRIVER=selenium but the selenium package is not "
                "installed. Install with `pip install crawlerkit[selenium]`."
            )
        # CRAWLERKIT_PROXY_URL is set by the worker; spiders can override
        # via kwargs.
        kwargs.setdefault("proxy", os.environ.get("CRAWLERKIT_PROXY_URL") or None)
        kwargs.setdefault("binary", os.environ.get("CRAWLERKIT_CHROME_BINARY") or None)
        return SeleniumDriver(stealth=stealth, headless=headless, **kwargs)

    # Auto: real if available, mock otherwise. We don't emit a warning here
    # — spider authors running on a laptop without Chromium installed *want*
    # the mock to keep working.
    if _selenium_importable():
        kwargs.setdefault("proxy", os.environ.get("CRAWLERKIT_PROXY_URL") or None)
        kwargs.setdefault("binary", os.environ.get("CRAWLERKIT_CHROME_BINARY") or None)
        return SeleniumDriver(stealth=stealth, headless=headless, **kwargs)
    return MockDriver(stealth=stealth, headless=headless)
