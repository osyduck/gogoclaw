import { expect, test } from "vitest";
import { formatAgo, formatExpiry } from "./Expiry";

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

test("formatAgo shows 'just now' just under a minute ago", () => {
  const now = 1_000_000;
  expect(formatAgo(now - 5, now)).toBe("just now");
});

test("formatAgo shows minutes ago", () => {
  const now = 1_000_000;
  expect(formatAgo(now - 300, now)).toBe("5m ago");
});

test("formatAgo shows hours ago", () => {
  const now = 1_000_000;
  expect(formatAgo(now - 7200, now)).toBe("2h ago");
});

test("formatAgo shows days ago", () => {
  const now = 1_000_000;
  expect(formatAgo(now - 172800, now)).toBe("2d ago");
});

test("formatAgo shows an em dash for a zero timestamp", () => {
  const now = 1_000_000;
  expect(formatAgo(0, now)).toBe("—");
});
