// Mounts an interactive widget into the element the server rendered for it.
// Navigation between pages is plain document navigation; there is no client router.
import { StrictMode, type ReactNode } from "react";
import { createRoot } from "react-dom/client";
import { onUnauthorized } from "../api/client";

export function loginUrl(): string {
  return "/login?next=" + encodeURIComponent(location.pathname + location.search);
}

/**
 * On a 401, "redirect" leaves for the login page; "notify" keeps the page (and any
 * unsent SQL) and offers the login page in a new tab instead.
 */
export function mountWidget(
  name: string,
  render: (el: HTMLElement) => ReactNode,
  on401: "redirect" | "notify" = "redirect",
): void {
  const el = document.querySelector<HTMLElement>(`[data-widget="${name}"]`);
  if (!el) return;
  onUnauthorized(on401 === "redirect" ? () => location.assign(loginUrl()) : showSessionExpired);
  createRoot(el).render(<StrictMode>{render(el)}</StrictMode>);
}

export function showSessionExpired(): void {
  if (document.getElementById("session-expired")) return;
  const notice = document.createElement("p");
  notice.id = "session-expired";
  notice.setAttribute("role", "alert");
  const link = document.createElement("a");
  link.href = loginUrl();
  link.target = "_blank";
  link.rel = "noopener";
  link.textContent = "Masuk lagi di tab baru";
  notice.append("Sesi Anda telah berakhir. Perubahan yang belum dikirim tetap ada di halaman ini. ", link);
  (document.querySelector("main") ?? document.body).prepend(notice);
}
