"""crawlerkit — spider SDK for crawler-lite.

Spider authors typically import:

    from crawlerkit import Spider, log, item, screenshot, captcha

The runner harness (`python -m crawlerkit.runner`) imports the user's spider
class from CRAWLERKIT_CONFIG.entry_module and calls `Spider().run()`.
"""

from ._events import captcha, item, log, screenshot
from .spider import Spider

__all__ = ["Spider", "log", "item", "screenshot", "captcha"]
__version__ = "0.3.0"
