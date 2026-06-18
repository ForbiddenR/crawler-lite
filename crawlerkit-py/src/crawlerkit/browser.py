"""Mock browser driver.

Stand-in for Selenium — same surface, no actual page loading. Lets spider
authors write `with self.driver() as d:` patterns now and have them work
unchanged once week 3 swaps in real Selenium.

The mock records every `get(url)` so tests can assert on navigation order,
returns deterministic placeholders for `find_element` and friends, and
generates a tiny 1x1 PNG when `get_screenshot_as_png` is called so the
screenshot upload pipeline can be exercised end-to-end without a browser.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any


# 1x1 transparent PNG — useful for testing the screenshot pipeline.
_TINY_PNG = (
    b"\x89PNG\r\n\x1a\n"
    b"\x00\x00\x00\rIHDR"
    b"\x00\x00\x00\x01\x00\x00\x00\x01"  # 1x1
    b"\x08\x06\x00\x00\x00"
    b"\x1f\x15\xc4\x89"
    b"\x00\x00\x00\rIDATx\x9cc\x00\x01\x00\x00\x05\x00\x01\r\n-\xb4"
    b"\x00\x00\x00\x00IEND\xaeB`\x82"
)


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
    """Selenium-shaped mock. Records visited URLs in `.history`."""

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
