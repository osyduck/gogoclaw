"""Drives OAuth consent in a stealth CloakBrowser to the callback redirect.

Async: cloakbrowser's Playwright runs on the sidecar's aiohttp event loop
(launch_async), so the whole drive is a coroutine. Using the sync API here would
trip Playwright's "Sync API inside the asyncio loop" guard.

Two providers:
- "google": Google's own consent page (email/password + consent).
- "zai": chat.z.ai brokers the Google login — an extra "Continue with Google"
  pre-step and a chat.z.ai authorize (ToS checkbox + Continue) post-step wrap
  the same Google steps.

Stateless: it knows nothing about tokens/state. The browser's redirect to
http://localhost:18432/** is captured by GogoClaw's own callback handler; this
module only reports whether the browser reached that point.
"""

CALLBACK_URL_GLOB = "http://localhost:18432/**"
BEST_EFFORT_MS = 2500

# Approve/continue buttons vary by the account's Google locale, plus a
# Workspace/Education "I understand" speedbump. Best-effort, never fatal.
CONSENT_SELECTORS = [
    "#submit_approve_access",
    "button:has-text('I understand')",
    "button:has-text('Continue')",
    "button:has-text('Allow')",
    "button:has-text('Lanjutkan')",   # id
    "button:has-text('Izinkan')",     # id
    "button:has-text('Continuar')",   # es/pt
    "button:has-text('Weiter')",      # de
    "button:has-text('Autoriser')",   # fr
]


async def _default_launcher(*, headless, humanize, proxy):
    # Imported lazily so tests (which inject a fake launcher) don't need the real
    # stealth Chromium download.
    from cloakbrowser import launch_async

    return await launch_async(headless=headless, humanize=humanize, proxy=proxy)


async def _click_best_effort(page, selector, timeout=BEST_EFFORT_MS):
    try:
        await page.locator(selector).click(timeout=timeout)
    except Exception:
        pass


async def _google_login(page, email, password):
    await page.locator("#identifierId").fill(email)
    await page.locator("#identifierNext").click()
    await page.locator('input[name="Passwd"]').fill(password)
    await page.locator("#passwordNext").click()
    # A consent/speedbump screen may or may not appear; best-effort, never fatal.
    for sel in CONSENT_SELECTORS:
        await _click_best_effort(page, sel)


async def drive(oauth_url, email, password, proxy=None, provider="google",
                launcher=_default_launcher, headless=True):
    """Drive the browser to the OAuth callback. Returns {"ok": True} or
    {"ok": False, "reason": "..."}."""
    browser = None
    try:
        # An empty proxy string (the Go side always sends the field) must become
        # None, else Playwright rejects it with "Invalid URL".
        browser = await launcher(headless=headless, humanize=True, proxy=proxy or None)
        page = await browser.new_page()
        await page.goto(oauth_url)
        if provider == "zai":
            # chat.z.ai login page → hand off to Google.
            await page.locator("button:has-text('Continue with Google')").click(timeout=BEST_EFFORT_MS * 4)
        await _google_login(page, email, password)
        if provider == "zai":
            # chat.z.ai authorize: Continue is disabled until ToS is ticked.
            try:
                await page.locator("input[type='checkbox']").check(timeout=BEST_EFFORT_MS)
            except Exception:
                pass
            await _click_best_effort(page, "button:has-text('Continue')", timeout=BEST_EFFORT_MS * 4)
        await page.wait_for_url(CALLBACK_URL_GLOB)
        return {"ok": True}
    except Exception as exc:  # wrong password, 2FA, captcha, timeout, …
        return {"ok": False, "reason": str(exc)}
    finally:
        if browser is not None:
            try:
                await browser.close()
            except Exception:
                pass
