# crawlerkit

Python SDK for crawler-lite spider authors.

## What it does

Spider authors write a class with one method: `run()`. The SDK provides:

- `Spider` base class with task config + args injected.
- `log()`, `item()`, `screenshot()`, `captcha()` — emit structured events to
  the worker over file descriptor 3.
- `Spider.driver()` — yields a real Chromium driver via Selenium when the
  optional `[selenium]` extra is installed, or a `MockDriver` stand-in
  otherwise. The mock keeps the same surface so tests and laptop-author
  workflows don't need a browser installed.

## Example

```python
from crawlerkit import Spider

class HelloSpider(Spider):
    def run(self):
        self.log("INFO", "starting")
        with self.driver() as d:
            d.get("https://example.com")
            self.screenshot("home", d.get_screenshot_as_png(), url=d.current_url)
            self.item({"title": d.title, "url": d.current_url})
        self.log("INFO", "done")
```

The worker invokes:

```
python -m crawlerkit.runner
```

with these env vars set:

| Var | Meaning |
|---|---|
| `CRAWLERKIT_TASK_ID` | int task id |
| `CRAWLERKIT_SPIDER_ID` | int spider id |
| `CRAWLERKIT_EVENT_FD` | file descriptor (= 3) for structured events |
| `CRAWLERKIT_CONFIG` | JSON: `{"entry_module": "spiders.hello:HelloSpider", "config": {...}}` |
| `CRAWLERKIT_ARGS` | JSON: run-now arguments |
| `CRAWLERKIT_PROXY_URL` | proxy URL or empty (forwarded to the driver) |
| `CRAWLERKIT_PRESIGN_PUT` | presigned PUT for screenshot uploads |
| `CRAWLERKIT_DRIVER` | `mock` \| `selenium` \| unset (auto) |
| `CRAWLERKIT_CHROME_BINARY` | optional path to a non-default Chrome/Chromium binary |

## Drivers

`Spider.driver()` honours `CRAWLERKIT_DRIVER`:

- **unset** (default): real Selenium if the `selenium` package is installed,
  otherwise the mock. Designed so authoring on a laptop without Chromium
  still works.
- **`mock`**: always the mock. Use this in unit tests.
- **`selenium`**: always real; raises if `selenium` isn't installed.

The real driver uses Chromium via Selenium 4. Selenium 4.6+ ships
[Selenium Manager](https://www.selenium.dev/blog/2022/introducing-selenium-manager/),
so `chromedriver` is auto-resolved against the installed Chrome — no
`webdriver-manager`, no vendored binaries.

### Stealth

The driver injects a small JS shim before page scripts run that hides the
most obvious automation signals (`navigator.webdriver`, plugin/language
defaults). It is **not** a full bot-detection bypass — sites with serious
fingerprinting still win. Spider authors who need stronger evasion can
override `Spider._make_driver` and pull in `undetected-chromedriver` and
`selenium-wire` directly in their spider repo's requirements.

## Install

From this directory in dev:

```sh
pip install -e .
```

> In production, the worker installs your spider's `requirements.txt` into a
> per-spider cached venv on first run — you don't need to install
> `crawlerkit` by hand on workers. Pin a specific version in your spider's
> `requirements.txt` if you need to.

For real Selenium support:

```sh
pip install -e .[selenium]
# plus a Chromium runtime — pick one for your platform:
#   apt-get install -y chromium       # Debian/Ubuntu (server / CI)
#   brew install --cask google-chrome # macOS dev
```

For tests:

```sh
pip install -e .[test]
pytest
```
