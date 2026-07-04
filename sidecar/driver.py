"""Drives Google OAuth consent in a stealth CloakBrowser to the callback redirect.

Stateless: it knows nothing about tokens/state. The browser's redirect to
http://localhost:18432/** is captured by GogoClaw's own callback handler; this
module only reports whether the browser reached that point.
"""

CALLBACK_URL_GLOB = "http://localhost:18432/**"
CONSENT_SELECTORS = [
    "button:has-text('Continue')",
    "button:has-text('Allow')",
    "#submit_approve_access",
]


def _default_launcher(*, headless, humanize, proxy):
    # Imported lazily so tests (which inject a fake launcher) don't need the real
    # stealth Chromium download.
    from cloakbrowser import launch

    return launch(headless=headless, humanize=humanize, proxy=proxy)


def drive(oauth_url, email, password, proxy=None, launcher=_default_launcher, headless=True):
    """Drive the browser to the OAuth callback. Returns {"ok": True} or
    {"ok": False, "reason": "..."}."""
    browser = launcher(headless=headless, humanize=True, proxy=proxy)
    try:
        page = browser.new_page()
        page.goto(oauth_url)
        page.locator("#identifierId").fill(email)
        page.locator("#identifierNext").click()
        page.locator('input[name="Passwd"]').fill(password)
        page.locator("#passwordNext").click()
        # A consent screen may or may not appear; best-effort, never fatal.
        for sel in CONSENT_SELECTORS:
            try:
                page.locator(sel).click()
            except Exception:
                pass
        page.wait_for_url(CALLBACK_URL_GLOB)
        return {"ok": True}
    except Exception as exc:  # wrong password, 2FA, captcha, timeout, …
        return {"ok": False, "reason": str(exc)}
    finally:
        try:
            browser.close()
        except Exception:
            pass
