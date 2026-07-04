import { expect, test } from "vitest";
import { formatExpiry } from "./Expiry";

test("formatExpiry shows hours+minutes in the future", () => {
  const now = 1_000_000;
  expect(formatExpiry(now + 3600 + 41 * 60, now)).toBe("1h 41m");
});

test("formatExpiry shows days+hours beyond a day", () => {
  const now = 1_000_000;
  expect(formatExpiry(now + 25 * 3600, now)).toBe("1d 1h");
});

test("formatExpiry shows 'expired' in the past", () => {
  const now = 1_000_000;
  expect(formatExpiry(now - 10, now)).toBe("expired");
});

test("formatExpiry shows an em dash for a zero timestamp", () => {
  expect(formatExpiry(0, 1_000_000)).toBe("—");
});

test("formatExpiry shows 'expired' at exactly zero delta", () => {
  const now = 1_000_000;
  expect(formatExpiry(now, now)).toBe("expired");
});

test("formatExpiry shows '1d 0h' at exactly 24 hours", () => {
  const now = 1_000_000;
  expect(formatExpiry(now + 24 * 3600, now)).toBe("1d 0h");
});
