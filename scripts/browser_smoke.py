#!/usr/bin/env python3
"""Exercise HTTPS setup, 2FA login, restore, and streaming in Chrome."""

import base64
import hashlib
import hmac
import json
import os
from pathlib import Path
import shutil
import struct
import subprocess
import time
from urllib.error import HTTPError, URLError
from urllib.request import Request, urlopen


PORT = 9515
BASE = f"http://127.0.0.1:{PORT}"
TARGET = os.environ.get("MIAO_TEST_BASE_URL", "http://127.0.0.1:18765")
ELEMENT = "element-6066-11e4-a52e-4f735466cecf"


def request(method, path, body=None):
    if method == "POST" and body is None:
        body = {}
    data = None if body is None else json.dumps(body).encode()
    req = Request(BASE + path, data=data, method=method)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urlopen(req, timeout=30) as response:
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


def totp(secret):
    key = base64.b32decode(secret)
    counter = struct.pack(">Q", int(time.time()) // 30)
    digest = hmac.new(key, counter, hashlib.sha1).digest()
    offset = digest[-1] & 15
    return f"{(struct.unpack('>I', digest[offset:offset + 4])[0] & 0x7fffffff) % 1000000:06d}"


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
                "args": ["--headless=new", "--no-sandbox", "--disable-dev-shm-usage", "--ignore-certificate-errors"]
            }}}
        })["sessionId"]
        root = f"/session/{session}"

        def find(selector):
            return request("POST", root + "/element", {"using": "css selector", "value": selector})[ELEMENT]

        def fill(selector, value):
            element = wait_for(lambda: find(selector))
            request("POST", root + f"/element/{element}/clear")
            request("POST", root + f"/element/{element}/value", {"text": value})

        def click(selector):
            element = wait_for(lambda: find(selector))
            request("POST", root + f"/element/{element}/click")

        def execute(script, *args):
            return request("POST", root + "/execute/sync", {"script": script, "args": list(args)})

        def execute_async(script):
            return request("POST", root + "/execute/async", {"script": script, "args": []})

        def click_text(text):
            return execute("""
              const text = arguments[0];
              const el = [...document.querySelectorAll('button, .side-item')].find(x => x.textContent.trim() === text);
              if (!el) return false;
              el.click();
              return true;
            """, text)

        def login(password, secret=""):
            fill(".login-card input[autocomplete='username']", "admin")
            fill(".login-card input[autocomplete='current-password']", password)
            click(".login-card .login-btn")
            if secret:
                wait_for(lambda: find(".login-card input[autocomplete='one-time-code']"))
                fill(".login-card input[autocomplete='one-time-code']", totp(secret))
                click(".login-card .login-btn")
            wait_for(lambda: find(".side-user button[title='退出登录']"))

        request("POST", root + "/url", {"url": TARGET + "/"})
        password = "browser smoke test password"
        secret_file = os.environ.get("MIAO_TEST_TOTP_FILE", "")
        mode = os.environ.get("MIAO_TEST_MODE", "setup")
        if mode == "setup":
            wait_for(lambda: find(".login-card input[placeholder='XXXX-XXXX-XXXX-XXXX']"))
            code_file = os.environ.get("MIAO_TEST_SETUP_CODE_FILE")
            code = (Path(code_file).read_text().strip() if code_file else
                    subprocess.check_output(["docker", "exec", "miaopanel", "cat", "/data/setup-code"], text=True).strip())
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
            login(password)
        else:
            wait_for(lambda: find(".login-card input[autocomplete='current-password']"))
            login(password, Path(secret_file).read_text().strip() if secret_file else "")

        cookies = request("GET", root + "/cookie")
        session_cookie = next(c for c in cookies if c["name"] == "miao_session")
        if TARGET.startswith("https://"):
            assert session_cookie["secure"] and session_cookie["httpOnly"] and session_cookie["sameSite"] == "Strict", session_cookie

        servers = execute_async("""
          const done = arguments[arguments.length - 1];
          fetch('/api/servers', {headers: {'X-Miao': '1'}})
            .then(r => r.json()).then(done).catch(e => done({error: String(e)}));
        """)
        if mode == "restored":
            assert any(s.get("name") == "restore-check" for s in servers), servers
            print("Browser password, TOTP, restored server list, and secure cookie passed")
            return

        created = execute_async("""
          const done = arguments[arguments.length - 1];
          fetch('/api/servers', {method: 'POST', headers: {'X-Miao': '1', 'Content-Type': 'application/json'},
            body: JSON.stringify({name: 'restore-check', host: '192.0.2.10', port: 22, username: 'root', authKind: 'password', password: 'restore-test-secret'})})
            .then(async r => done({status: r.status, body: await r.text()})).catch(e => done({error: String(e)}));
        """)
        assert created.get("status") == 200, created

        if os.environ.get("MIAO_TEST_ENABLE_TOTP") == "1":
            assert click_text("设置")
            wait_for(lambda: click_text("开启两步验证"))
            fill("input[aria-label='密码']", password)
            assert click_text("继续")
            secret = wait_for(lambda: execute("return document.querySelector('.totp-secret')?.textContent.trim() || ''"))
            fill(".totp-confirm input", totp(secret))
            assert click_text("确认开启")
            wait_for(lambda: execute("return document.body.textContent.includes('已开启：登录时还要输入手机 App 里的验证码')"))
            if secret_file:
                secret_path = Path(secret_file)
                secret_path.write_text(secret)
                secret_path.chmod(0o600)
            click(".side-user button[title='退出登录']")
            wait_for(lambda: find(".login-card input[autocomplete='current-password']"))
            login(password, secret)

        execute("""
          document.querySelectorAll('.side-item').forEach(el => {
            if (el.textContent.trim() === 'AI 助手') el.click();
          });
          const realFetch = window.fetch;
          window.fetch = (url, options) => {
            if (url !== '/api/chat/stream') return realFetch(url, options);
            let count = 0;
            let fullText = '';
            const encoder = new TextEncoder();
            const stream = new ReadableStream({
              start(controller) {
                const timer = setInterval(() => {
                  if (count < 80) {
                    const text = `Paragraph ${count}: ${'x'.repeat(200)}\\n\\n`;
                    fullText += text;
                    controller.enqueue(encoder.encode(JSON.stringify({type: 'text', text}) + '\\n'));
                    count++;
                  } else {
                    controller.enqueue(encoder.encode(JSON.stringify({type: 'done', reply: {
                      conversationId: 'browser-smoke', reply: {text: fullText}, plans: []
                    }}) + '\\n'));
                    clearInterval(timer);
                    controller.close();
                  }
                }, 80);
              }
            });
            return Promise.resolve(new Response(stream, {status: 200}));
          };
        """)
        fill(".chat textarea", "Test scrolling while the answer streams")
        click(".chat button.send.primary")
        wait_for(lambda: execute("""
          const box = document.querySelector('.messages');
          return box.scrollHeight > box.clientHeight + 200 ? box.scrollHeight : 0;
        """))
        text_length = execute("return document.querySelector('.live-text')?.textContent.length || 0")
        execute("""
          const box = document.querySelector('.messages');
          box.scrollTop = 0;
          box.dispatchEvent(new Event('scroll'));
        """)
        wait_for(lambda: execute(f"return (document.querySelector('.live-text')?.textContent.length || 0) > {text_length + 300}"))
        assert execute("return document.querySelector('.messages').scrollTop") <= 4, "Streaming pulled the reader away from earlier text"
        wait_for(lambda: execute("return !document.querySelector('.chat button.send.stop')"), seconds=15)
        assert execute("return document.querySelector('.messages').scrollTop") <= 4, "Answer completion jumped to the bottom"
        print("Browser setup, password/TOTP login, secure cookie, restore seed, and chat scrolling passed")
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
