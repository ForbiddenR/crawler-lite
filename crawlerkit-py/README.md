# crawlerkit

Python SDK for crawler-lite spider authors.

## What it does

Spider authors write a class with one method: `run()`. The SDK provides:

- `Spider` base class with task config + args injected.
- `log()`, `item()`, `screenshot()`, `captcha()` — emit structured events to
  the worker over file descriptor 3.
- `MockDriver` — a stand-in for Selenium that lets you author and test
  spiders before week-3 brings the real browser stack.

## Example

```python
from crawlerkit import Spider, log, item, screenshot

class HelloSpider(Spider):
    def run(self):
        log("INFO", "starting")
        with self.driver() as d:
            d.get("https://example.com")
            screenshot("home", d.get_screenshot_as_png(), url=d.current_url)
            item({"title": d.title})
        log("INFO", "done")
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
| `CRAWLERKIT_PROXY_URL` | proxy URL or empty |
| `CRAWLERKIT_PRESIGN_PUT` | presigned PUT for screenshot uploads |

## Install

From this directory in dev:

```sh
pip install -e .
```

For real Selenium support (week 3+):

```sh
pip install -e .[selenium]
```
