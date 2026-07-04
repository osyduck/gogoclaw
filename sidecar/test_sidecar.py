import asyncio

import pytest
from sidecar.driver import drive
from sidecar.server import make_app


class FakeLocator:
    def __init__(self, calls, sel):
        self.calls = calls
        self.sel = sel

    async def fill(self, value, **kw):
        self.calls.append(("fill", self.sel, value))

    async def click(self, **kw):
        self.calls.append(("click", self.sel))

    async def check(self, **kw):
        self.calls.append(("check", self.sel))


class FakePage:
    def __init__(self, calls, fail_on_wait=False):
        self.calls = calls
        self.fail_on_wait = fail_on_wait

    async def goto(self, url):
        self.calls.append(("goto", url))

    def locator(self, sel):  # locator() is synchronous in Playwright
        return FakeLocator(self.calls, sel)

    async def wait_for_url(self, glob, **kw):
        if self.fail_on_wait:
            raise RuntimeError("navigation timeout")
        self.calls.append(("wait_for_url", glob))


class FakeBrowser:
    def __init__(self, calls, fail_on_wait=False):
        self.calls = calls
        self.fail_on_wait = fail_on_wait
        self.closed = False

    async def new_page(self):
        return FakePage(self.calls, self.fail_on_wait)

    async def close(self):
        self.closed = True


def async_launcher(browser):
    async def _launch(**kw):
        return browser
    return _launch


def test_drive_success_fills_and_waits():
    calls = []
    browser = FakeBrowser(calls)
    result = asyncio.run(drive("https://g/o", "a@x.com", "pw", launcher=async_launcher(browser)))
    assert result == {"ok": True}
    # email + password were filled and the callback redirect was awaited
    assert ("fill", "#identifierId", "a@x.com") in calls
    assert ("fill", 'input[name="Passwd"]', "pw") in calls
    assert any(c[0] == "wait_for_url" for c in calls)
    assert browser.closed is True  # browser always closed


def test_drive_failure_returns_reason():
    browser = FakeBrowser([], fail_on_wait=True)
    result = asyncio.run(drive("https://g/o", "a@x.com", "pw", launcher=async_launcher(browser)))
    assert result["ok"] is False
    assert "timeout" in result["reason"]
    assert browser.closed is True


def test_drive_launcher_failure_returns_reason():
    async def boom(**kw):
        raise RuntimeError("chromium launch failed")
    result = asyncio.run(drive("https://g/o", "a@x.com", "pw", launcher=boom))
    assert result["ok"] is False
    assert "chromium launch failed" in result["reason"]


def test_drive_normalizes_empty_proxy_to_none():
    # The Go side always sends "proxy" (empty string when none); passing "" to
    # cloakbrowser's launch makes Playwright reject it as an Invalid URL.
    seen = {}

    async def launcher(**kw):
        seen.update(kw)
        return FakeBrowser([])

    asyncio.run(drive("u", "a@x.com", "pw", proxy="", launcher=launcher))
    assert seen["proxy"] is None


def test_drive_zai_clicks_google_then_authorizes():
    calls = []
    browser = FakeBrowser(calls)
    result = asyncio.run(drive("https://chat.z.ai/x", "a@x.com", "pw", provider="zai",
                               launcher=async_launcher(browser)))
    assert result == {"ok": True}
    sels = [c[1] for c in calls if c[0] == "click"]
    # pre-step: continue with Google on chat.z.ai
    assert any("Continue with Google" in s for s in sels)
    # Google login still happens
    assert ("fill", "#identifierId", "a@x.com") in calls
    # post-step: chat.z.ai ToS checkbox is ticked and its Continue is clicked
    assert any(c[0] == "check" for c in calls)
    assert any(s == "button:has-text('Continue')" for s in sels)
    assert any(c[0] == "wait_for_url" for c in calls)


async def test_drive_endpoint_ok(aiohttp_client):
    app = make_app(drive_fn=lambda oauth_url, email, password, proxy=None, provider="google": {"ok": True})
    client = await aiohttp_client(app)
    resp = await client.post("/drive", json={"oauth_url": "u", "email": "a@x.com", "password": "pw"})
    assert resp.status == 200
    assert await resp.json() == {"ok": True}


async def test_drive_endpoint_missing_field(aiohttp_client):
    app = make_app(drive_fn=lambda **kw: {"ok": True})
    client = await aiohttp_client(app)
    resp = await client.post("/drive", json={"email": "a@x.com"})
    assert resp.status == 400


async def test_drive_endpoint_non_dict_body(aiohttp_client):
    app = make_app(drive_fn=lambda **kw: {"ok": True})
    client = await aiohttp_client(app)
    resp = await client.post("/drive", json=["not", "a", "dict"])
    assert resp.status == 400
    resp2 = await client.post("/drive", json=42)
    assert resp2.status == 400


async def test_health(aiohttp_client):
    client = await aiohttp_client(make_app())
    resp = await client.get("/health")
    assert resp.status == 200
    assert (await resp.json())["ok"] is True
