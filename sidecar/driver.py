"""Drives OAuth consent in a stealth CloakBrowser to the callback redirect.

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


def _default_launcher(*, headless, humanize, proxy):
    # Imported lazily so tests (which inject a fake launcher) don't need the real
    # stealth Chromium download.
    from cloakbrowser import launch

    return launch(headless=headless, humanize=humanize, proxy=proxy)


def _best_effort(fn):
    try:
        fn()
    except Exception:
        pass


def _google_login(page, email, password):
    page.locator("#identifierId").fill(email)
    page.locator("#identifierNext").click()
    page.locator('input[name="Passwd"]').fill(password)
    page.locator("#passwordNext").click()
    # A consent/speedbump screen may or may not appear; best-effort, never fatal.
    for sel in CONSENT_SELECTORS:
        _best_effort(lambda sel=sel: page.locator(sel).click(timeout=BEST_EFFORT_MS))


def drive(oauth_url, email, password, proxy=None, provider="google",
          launcher=_default_launcher, headless=True):
    """Drive the browser to the OAuth callback. Returns {"ok": True} or
    {"ok": False, "reason": "..."}."""
    browser = None
    try:
        browser = launcher(headless=headless, humanize=True, proxy=proxy)
        page = browser.new_page()
        page.goto(oauth_url)
        if provider == "zai":
            # chat.z.ai login page → hand off to Google.
            page.locator("button:has-text('Continue with Google')").click(timeout=BEST_EFFORT_MS * 4)
        _google_login(page, email, password)
        if provider == "zai":
            # chat.z.ai authorize: Continue is disabled until ToS is ticked.
            _best_effort(lambda: page.locator("input[type='checkbox']").check(timeout=BEST_EFFORT_MS))
            _best_effort(lambda: page.locator("button:has-text('Continue')").click(timeout=BEST_EFFORT_MS * 4))
        page.wait_for_url(CALLBACK_URL_GLOB)
        return {"ok": True}
    except Exception as exc:  # wrong password, 2FA, captcha, timeout, …
        return {"ok": False, "reason": str(exc)}
    finally:
        if browser is not None:
            try:
                browser.close()
            except Exception:
                pass
