"""aiohttp server exposing the sidecar's /drive and /health endpoints.

/drive streams newline-delimited JSON: one {"step": "..."} line per action as
it happens, then a final {"done": true, "ok": ..., "reason": ...} line. This
lets the dashboard show a live step-by-step terminal for each login.
"""
import inspect
import json

from aiohttp import web

from .driver import drive as default_drive

REQUIRED_FIELDS = ("oauth_url", "email", "password")


async def handle_drive(request):
    try:
        body = await request.json()
    except Exception:
        return web.json_response({"ok": False, "reason": "invalid json"}, status=400)
    if not isinstance(body, dict):
        return web.json_response({"ok": False, "reason": "body must be a JSON object"}, status=400)
    missing = [f for f in REQUIRED_FIELDS if f not in body]
    if missing:
        return web.json_response({"ok": False, "reason": f"missing: {', '.join(missing)}"}, status=400)

    resp = web.StreamResponse()
    resp.headers["content-type"] = "application/x-ndjson"
    await resp.prepare(request)

    async def emit(line):
        await resp.write((json.dumps({"step": line}) + "\n").encode())

    drive_fn = request.app["drive"]
    try:
        # The real driver is async (cloakbrowser runs on this event loop);
        # test stubs may return a plain dict — accept both.
        result = drive_fn(body["oauth_url"], body["email"], body["password"],
                          body.get("proxy"), provider=body.get("provider", "google"), emit=emit)
        if inspect.isawaitable(result):
            result = await result
    except Exception as exc:
        result = {"ok": False, "reason": str(exc)}
    await resp.write((json.dumps({"done": True, **result}) + "\n").encode())
    await resp.write_eof()
    return resp


async def handle_health(_request):
    return web.json_response({"ok": True})


def make_app(drive_fn=default_drive):
    app = web.Application()
    app["drive"] = drive_fn
    app.router.add_post("/drive", handle_drive)
    app.router.add_get("/health", handle_health)
    return app


def main():
    web.run_app(make_app(), host="127.0.0.1", port=31500)
