/**
 * The post-login target from ?next=: only a path on this site, so a crafted
 * link cannot send the user elsewhere ("//evil", "https://evil", "/\evil").
 */
export function safeNext(value: unknown): string | undefined {
  if (typeof value !== "string" || !value.startsWith("/") || value.startsWith("//") || value.startsWith("/\\")) {
    return undefined;
  }
  try {
    const url = new URL(value, "http://local.invalid");
    return url.origin === "http://local.invalid" ? url.pathname + url.search + url.hash : undefined;
  } catch {
    return undefined;
  }
}
