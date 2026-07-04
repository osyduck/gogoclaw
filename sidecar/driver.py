"""Drives OAuth consent in a stealth CloakBrowser to the callback redirect.

Async: cloakbrowser's Playwright runs on the sidecar's aiohttp event loop
(launch_async), so the whole drive is a coroutine. Using the sync API here would
trip Playwright's "Sync API inside the asyncio loop" guard.

Two providers:
- "google": Google's own consent page (email/password + consent).
- "zai": chat.z.ai brokers the Google login — an extra "Continue with Google"
  pre-step and a chat.z.ai authorize (ToS checkbox + Continue) post-step wrap
  the same Google steps.

After the password, Google can show a variable sequence of screens (a
Workspace/Education "I understand" speedbump, then an OAuth consent), and for
zai a final chat.z.ai authorize page. Those are walked by a URL-driven state
machine (`_advance`) that waits for each screen instead of racing navigation.

Progress: each action is announced through the optional `emit` async callback
(timestamped), so the UI can show a live step-by-step terminal and see exactly
which step stalled. Stateless otherwise: the browser's redirect to
http://localhost:18432/** is captured by GogoClaw's own callback handler; this
module only reports whether the browser reached that point.
"""
import re
import time

CALLBACK_PREFIX = "http://localhost:18432/"
CALLBACK_URL_GLOB = "http://localhost:18432/**"

# Any of these accessible button names advances a Google speedbump/consent
# screen; the name varies by the account's locale.
CONSENT_NAME = re.compile(
    r"I understand|Continue|Allow|Confirm|Lanjutkan|Izinkan|Konfirmasi|"
    r"Continuar|Weiter|Autoriser|Aceptar|同意|同意する",
    re.I,
)


async def _default_launcher(*, headless, humanize, proxy):
    # Imported lazily so tests (which inject a fake launcher) don't need the real
    # stealth Chromium download.
    from cloakbrowser import launch_async

    return await launch_async(headless=headless, humanize=humanize, proxy=proxy)


async def _advance(page, provider, step):
    """Walk the post-password screens until the browser reaches the callback.

    Each iteration inspects the current URL and either handles the chat.z.ai
    authorize page (zai), clicks a Google speedbump/consent button, or waits for
    the next navigation. Bounded so a stuck flow fails fast with a clear step.
    """
    for _ in range(8):
        url = page.url
        if url.startswith(CALLBACK_PREFIX):
            return
        if provider == "zai" and "/auth/oauth/authorize" in url:
            # chat.z.ai authorize: Continue is disabled until the ToS role=checkbox
            # is ticked. Click (don't .check(), which hangs on is_checked here).
            cb = page.get_by_role("checkbox").first
            await cb.wait_for(state="visible", timeout=8000)
            await cb.click()
            await page.get_by_role("button", name="Continue").first.click(timeout=8000)
            await step("authorized chat.z.ai")
            continue
        # A Google speedbump ("I understand") or OAuth consent — click whatever
        # matching button is on screen, waiting for it to appear.
        try:
            btn = page.get_by_role("button", name=CONSENT_NAME).first
            await btn.wait_for(state="visible", timeout=6000)
            await btn.click()
            await step(f"consent: {(await btn.inner_text()).strip()[:24]}")
            continue
        except Exception:
            pass
        # Nothing actionable yet — give the flow a moment to navigate, then recheck.
        try:
            await page.wait_for_load_state("networkidle", timeout=4000)
        except Exception:
            return


async def drive(oauth_url, email, password, proxy=None, provider="google",
                launcher=_default_launcher, headless=True, emit=None):
    """Drive the browser to the OAuth callback, announcing each step through
    `emit`. Returns {"ok": True} or {"ok": False, "reason": "..."}."""
    t0 = time.monotonic()

    async def step(msg):
        if emit is not None:
            await emit(f"[{time.monotonic() - t0:5.1f}s] {msg}")

    browser = None
    try:
        await step(f"launch stealth browser (provider={provider})")
        # An empty proxy string (the Go side always sends the field) must become
        # None, else Playwright rejects it with "Invalid URL".
        browser = await launcher(headless=headless, humanize=True, proxy=proxy or None)
        page = await browser.new_page()
        await step("open authorize page")
        await page.goto(oauth_url)
        if provider == "zai":
            await step("click 'Continue with Google'")
            await page.locator("button:has-text('Continue with Google')").click(timeout=15000)
        await step("enter email")
        await page.locator("#identifierId").fill(email)
        await page.locator("#identifierNext").click()
        await step("enter password")
        await page.locator('input[name="Passwd"]').fill(password)
        await page.locator("#passwordNext").click()
        await step("walk consent / authorize screens")
        await _advance(page, provider, step)
        await step("wait for callback redirect")
        await page.wait_for_url(CALLBACK_URL_GLOB, timeout=45000)
        await step("callback reached OK")
        return {"ok": True}
    except Exception as exc:  # wrong password, 2FA, captcha, timeout, …
        await step(f"ERROR: {exc}")
        return {"ok": False, "reason": str(exc)}
    finally:
        if browser is not None:
            try:
                await browser.close()
            except Exception:
                pass
