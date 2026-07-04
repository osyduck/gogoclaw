// formatExpiry renders a compact relative time-to-expiry. 0 => em dash; past => "expired".
export function formatExpiry(atUnixSeconds: number, nowUnixSeconds = Math.floor(Date.now() / 1000)): string {
  if (atUnixSeconds === 0) return "—";
  const delta = atUnixSeconds - nowUnixSeconds;
  if (delta <= 0) return "expired";
  const h = Math.floor(delta / 3600);
  const m = Math.floor((delta % 3600) / 60);
  if (h >= 24) {
    const d = Math.floor(h / 24);
    return `${d}d ${h % 24}h`;
  }
  return `${h}h ${m}m`;
}

export function Expiry({ at }: { at: number }) {
  return <span className="tabular-nums text-muted">{formatExpiry(at)}</span>;
}

// formatAgo renders a compact relative time-since. 0 => em dash; otherwise how long ago `at` was.
export function formatAgo(atUnixSeconds: number, nowUnixSeconds = Math.floor(Date.now() / 1000)): string {
  if (atUnixSeconds === 0) return "—";
  const delta = nowUnixSeconds - atUnixSeconds;
  if (delta < 60) return "just now";
  const m = Math.floor(delta / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(delta / 3600);
  if (h < 24) return `${h}h ago`;
  const d = Math.floor(h / 24);
  return `${d}d ago`;
}

export function Ago({ at }: { at: number }) {
  return <span className="tabular-nums text-muted">{formatAgo(at)}</span>;
}
