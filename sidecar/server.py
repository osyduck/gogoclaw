"""aiohttp server exposing the sidecar's /drive and /health endpoints."""
import asyncio

from aiohttp import web

from .driver import drive as default_drive

REQUIRED_FIELDS = ("oauth_url", "email", "password")


async def handle_drive(request):
    try:
        body = await request.json()
    except Exception:
        return web.json_response({"ok": False, "reason": "invalid json"}, status=400)
    missing = [f for f in REQUIRED_FIELDS if f not in body]
    if missing:
        return web.json_response({"ok": False, "reason": f"missing: {', '.join(missing)}"}, status=400)

    drive_fn = request.app["drive"]
    loop = asyncio.get_running_loop()
    result = await loop.run_in_executor(
        None,
        lambda: drive_fn(body["oauth_url"], body["email"], body["password"], body.get("proxy")),
    )
    return web.json_response(result)


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
