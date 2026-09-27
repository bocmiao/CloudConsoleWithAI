#!/usr/bin/env python3
"""Exercise first-run setup and password login in a real browser."""

import json
import os
from pathlib import Path
import shutil
import subprocess
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


PORT = 9515
BASE = f"http://127.0.0.1:{PORT}"
ELEMENT = "element-6066-11e4-a52e-4f735466cecf"


def request(method, path, body=None):
    if method == "POST" and body is None:
        body = {}
    data = None if body is None else json.dumps(body).encode()
    req = Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urlopen(req, timeout=10) as response:
            result = json.load(response)
    except HTTPError as error:
        raise HTTPError(error.url, error.code, error.read().decode(), error.headers, None) from error
    value = result["value"]
    if isinstance(value, dict) and value.get("error"):
        raise RuntimeError(value)
    return value


def wait_for(find, seconds=20):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        try:
            result = find()
            if result:
                return result
        except (HTTPError, URLError):
            pass
        time.sleep(0.25)
    raise TimeoutError("Browser condition did not appear in time")


def main():
    driver = shutil.which("chromedriver")
    if not driver:
        driver = str(Path(os.environ.get("CHROMEWEBDRIVER", "/usr/local/share/chromedriver-linux64")) / "chromedriver")
    process = subprocess.Popen([driver, f"--port={PORT}"], stdout=subprocess.DEVNULL)
    session = None
    try:
        wait_for(lambda: request("GET", "/status")["ready"])
        session = request("POST", "/session", {
            "capabilities": {"alwaysMatch": {"browserName": "chrome", "goog:chromeOptions": {
                "args": ["--headless=new", "--no-sandbox", "--disable-dev-shm-usage"]
            }}}
        })["sessionId"]
        root = f"/session/{session}"

        def find(selector):
            return request("POST", root + "/element", {"using": "css selector", "value": selector})[ELEMENT]

        def fill(selector, value):
            element = wait_for(lambda: find(selector))
            request("POST", root + f"/element/{element}/value", {"text": value})

        def click(selector):
            element = wait_for(lambda: find(selector))
            request("POST", root + f"/element/{element}/click")

        request("POST", root + "/url", {"url": "http://127.0.0.1:18765/"})
        wait_for(lambda: find(".login-card input[placeholder='XXXX-XXXX-XXXX-XXXX']"))
        code = subprocess.check_output(["docker", "exec", "miaopanel", "cat", "/data/setup-code"], text=True).strip()
        password = "browser smoke test password"
        fill(".login-card input[placeholder='XXXX-XXXX-XXXX-XXXX']", code)
        fill(".login-card input[autocomplete='username']", "admin")
        fields = request("POST", root + "/elements", {"using": "css selector", "value": ".login-card input[autocomplete='new-password']"})
        assert len(fields) == 2, "Expected setup and confirmation password fields"
        for field in fields:
            request("POST", root + f"/element/{field[ELEMENT]}/value", {"text": password})
        click(".login-card .login-btn")
        wait_for(lambda: find(".side-user button[title='退出登录']"))
        click(".side-user button[title='退出登录']")
        wait_for(lambda: find(".login-card input[autocomplete='current-password']"))
        fill(".login-card input[autocomplete='username']", "admin")
        fill(".login-card input[autocomplete='current-password']", password)
        click(".login-card .login-btn")
        wait_for(lambda: find(".side-user button[title='退出登录']"))
        print("Browser setup, logout, and password login passed")
    finally:
        if session:
            try:
                request("DELETE", f"/session/{session}")
            except (HTTPError, URLError):
                pass
        process.terminate()
        process.wait(timeout=5)


if __name__ == "__main__":
    main()
