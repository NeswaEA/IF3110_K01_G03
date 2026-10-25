import { ApiRequestError, request } from "../../api/client";
import { markInvalid } from "../../components/formFeedback";
import type { User } from "../../api/types";

export function bindUsers(root: HTMLElement, navigate: (url: string) => void): void {
  const forms = [...root.querySelectorAll<HTMLFormElement>("form[data-operation]")];
  const returnTo = root.dataset.returnTo || "/admin/users";
  let pending = false;

  for (const form of forms) {
    const fieldset = form.querySelector<HTMLFieldSetElement>("fieldset")!;
    const message = form.querySelector<HTMLElement>('[role="status"]')!;
    const password = form.querySelector<HTMLInputElement>('[name="password"]');
    const button = form.querySelector<HTMLButtonElement>('button[type="submit"]')!;
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (pending || !form.reportValidity()) return;
      delete message.dataset.tone;
      markInvalid(form, []);
      if (password && (Array.from(password.value).length < 12 || Array.from(password.value).length > 128)) {
        message.textContent = "Password harus berisi 12 sampai 128 karakter.";
        message.dataset.tone = "error";
        markInvalid(form, ["password"]);
        return;
      }
      const values = new FormData(form);
      const operation = form.dataset.operation!;
      const path = "/admin/users" + (operation === "create" ? "" : "/" + form.dataset.userId);
      pending = true;
      root.setAttribute("aria-busy", "true");
      for (const other of forms) other.querySelector<HTMLFieldSetElement>("fieldset")!.disabled = true;
      message.textContent = operation === "password" ? "Mengganti password..." : "Menyimpan pengguna...";

      try {
        if (operation === "create") {
          await request<User>(path, { method: "POST", body: {
            username: values.get("username"), password: values.get("password"),
            is_platform_admin: values.has("is_platform_admin"),
          } });
        } else if (operation === "update") {
          await request<User>(path, { method: "PATCH", body: {
            active: values.has("active"), is_platform_admin: values.has("is_platform_admin"),
          } });
        } else {
          await request(path + "/password", { method: "PUT", body: { password: values.get("password") } });
        }
        if (password) password.value = "";
        message.textContent = "Perubahan tersimpan. Memuat halaman...";
        const next = new URL(returnTo, location.origin);
        if (operation === "create") next.searchParams.delete("user");
        next.searchParams.set("notice", operation === "create" ? "created" : operation === "update" ? "updated" : "password");
        navigate(next.pathname + next.search);
      } catch (err) {
        if (err instanceof ApiRequestError && err.status === 401) {
          if (password) password.value = "";
          message.textContent = "Sesi berakhir. Membuka halaman login...";
          navigate("/login?next=" + encodeURIComponent(returnTo));
          return;
        }
        message.textContent = err instanceof ApiRequestError
          ? err.code === "CSRF_REJECTED"
            ? "Token CSRF kedaluwarsa. Silakan kirim formulir lagi."
            : [err.message, ...Object.values(err.fields)].filter(Boolean).join(" ")
          : "Perubahan gagal disimpan. Silakan coba lagi.";
        pending = false;
        root.removeAttribute("aria-busy");
        for (const other of forms) other.querySelector<HTMLFieldSetElement>("fieldset")!.disabled = false;
        message.dataset.tone = "error";
        markInvalid(form, err instanceof ApiRequestError ? Object.keys(err.fields) : []);
      }
    });
    button.disabled = false;
    fieldset.disabled = false;
  }
}
