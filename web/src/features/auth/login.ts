import { ApiRequestError, request, setCsrfToken } from "../../api/client";

export function bindLogin(form: HTMLFormElement, navigate: (url: string) => void): void {
  const fields = form.querySelector<HTMLFieldSetElement>("fieldset")!;
  const button = form.querySelector<HTMLButtonElement>('button[type="submit"]')!;
  const message = form.querySelector<HTMLElement>("#login-message")!;
  const password = form.querySelector<HTMLInputElement>('[name="password"]')!;
  let pending = false;
  button.disabled = false;

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    if (pending || !form.reportValidity()) return;

    const values = new FormData(form);
    pending = true;
    fields.disabled = true;
    form.setAttribute("aria-busy", "true");
    message.textContent = "Memeriksa akun...";

    try {
      await request("/auth/login", {
        method: "POST",
        body: { username: values.get("username"), password: values.get("password") },
      });
      setCsrfToken(null);
      password.value = "";
      message.textContent = "Berhasil masuk. Membuka halaman...";
      navigate(form.dataset.next || "/databases");
    } catch (err) {
      message.textContent = err instanceof ApiRequestError
        ? err.status === 401
          ? "Username atau password tidak sesuai, atau akun tidak aktif."
          : err.code === "CSRF_REJECTED"
            ? "Sesi login kedaluwarsa. Silakan coba masuk lagi."
            : err.message
        : "Tidak dapat masuk. Silakan coba lagi.";
      fields.disabled = false;
      form.removeAttribute("aria-busy");
      pending = false;
    }
  });
}
