import { ApiRequestError, request } from "../api/client";

/** Wires the layout's logout button; navigate is injected so tests need no real navigation. */
export function bindLogout(button: HTMLButtonElement, navigate: (url: string) => void): void {
  button.addEventListener("click", async () => {
    button.disabled = true;
    try {
      await request("/auth/logout", { method: "POST", body: {} });
    } catch (err) {
      // A 401 means the session is already gone, which is what logout wanted.
      if (!(err instanceof ApiRequestError && err.status === 401)) {
        button.disabled = false;
        button.setAttribute("aria-describedby", showLogoutError(button, err));
        return;
      }
    }
    navigate("/login");
  });
}

function showLogoutError(button: HTMLButtonElement, err: unknown): string {
  const id = "logout-error";
  const message = document.getElementById(id) ?? document.createElement("span");
  message.id = id;
  message.setAttribute("role", "alert");
  message.textContent = `Gagal keluar: ${err instanceof ApiRequestError ? err.message : "terjadi kesalahan."}`;
  button.after(message);
  return id;
}
