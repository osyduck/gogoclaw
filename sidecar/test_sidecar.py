import pytest
from sidecar.driver import drive
from sidecar.server import make_app


class FakeLocator:
    def __init__(self, calls, sel):
        self.calls = calls
        self.sel = sel

    def fill(self, value):
        self.calls.append(("fill", self.sel, value))

    def click(self):
        self.calls.append(("click", self.sel))


class FakePage:
    def __init__(self, calls, fail_on_wait=False):
        self.calls = calls
        self.fail_on_wait = fail_on_wait

    def goto(self, url):
        self.calls.append(("goto", url))

    def locator(self, sel):
        return FakeLocator(self.calls, sel)

    def wait_for_url(self, glob, **kw):
        if self.fail_on_wait:
            raise RuntimeError("navigation timeout")
        self.calls.append(("wait_for_url", glob))


class FakeBrowser:
    def __init__(self, calls, fail_on_wait=False):
        self.calls = calls
        self.fail_on_wait = fail_on_wait
        self.closed = False

    def new_page(self):
        return FakePage(self.calls, self.fail_on_wait)

    def close(self):
        self.closed = True


def test_drive_success_fills_and_waits():
    calls = []
    browser = FakeBrowser(calls)
    result = drive("https://g/o", "a@x.com", "pw", launcher=lambda **kw: browser)
    assert result == {"ok": True}
    # email + password were filled and the callback redirect was awaited
    assert ("fill", "#identifierId", "a@x.com") in calls
    assert ("fill", 'input[name="Passwd"]', "pw") in calls
    assert any(c[0] == "wait_for_url" for c in calls)
    assert browser.closed is True  # browser always closed


def test_drive_failure_returns_reason():
    browser = FakeBrowser([], fail_on_wait=True)
    result = drive("https://g/o", "a@x.com", "pw", launcher=lambda **kw: browser)
    assert result["ok"] is False
    assert "timeout" in result["reason"]
    assert browser.closed is True


def test_drive_launcher_failure_returns_reason():
    def boom(**kw):
        raise RuntimeError("chromium launch failed")
    result = drive("https://g/o", "a@x.com", "pw", launcher=boom)
    assert result["ok"] is False
    assert "chromium launch failed" in result["reason"]


async def test_drive_endpoint_ok(aiohttp_client):
    app = make_app(drive_fn=lambda oauth_url, email, password, proxy=None: {"ok": True})
    client = await aiohttp_client(app)
    resp = await client.post("/drive", json={"oauth_url": "u", "email": "a@x.com", "password": "pw"})
    assert resp.status == 200
    assert await resp.json() == {"ok": True}


async def test_drive_endpoint_missing_field(aiohttp_client):
    app = make_app(drive_fn=lambda **kw: {"ok": True})
    client = await aiohttp_client(app)
    resp = await client.post("/drive", json={"email": "a@x.com"})
    assert resp.status == 400


async def test_health(aiohttp_client):
    client = await aiohttp_client(make_app())
    resp = await client.get("/health")
    assert resp.status == 200
    assert (await resp.json())["ok"] is True
