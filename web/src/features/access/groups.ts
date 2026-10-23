import { ApiRequestError, request } from "../../api/client";
import type { Group } from "../../api/types";

export function bindGroups(root: HTMLElement, navigate: (url: string) => void): void {
  const forms = [...root.querySelectorAll<HTMLFormElement>("form[data-operation]")];
  const returnTo = root.dataset.returnTo || "/admin/groups";
  let pending = false;

  for (const form of forms) {
    const message = form.querySelector<HTMLElement>('[role="status"]')!;
    const name = form.querySelector<HTMLInputElement>('[name="name"]');
    const button = form.querySelector<HTMLButtonElement>('button[type="submit"]')!;
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (pending || !form.reportValidity()) return;
      const value = name?.value.replace(/^\p{White_Space}+|\p{White_Space}+$/gu, "") ?? "";
      if (name && (Array.from(value).length < 1 || Array.from(value).length > 100 || value.includes("\0"))) {
        message.textContent = "Nama grup harus berisi 1 sampai 100 karakter tanpa karakter NUL.";
        name.focus();
        return;
      }
      const operation = form.dataset.operation!;
      const path = "/admin/groups" + (operation === "create" ? "" : "/" + form.dataset.groupId);
      pending = true;
      root.setAttribute("aria-busy", "true");
      for (const other of forms) other.querySelector<HTMLFieldSetElement>("fieldset")!.disabled = true;
      message.textContent = operation === "delete" ? "Menghapus grup..." : "Menyimpan grup...";

      try {
        if (operation === "delete") {
          await request(path, { method: "DELETE" });
        } else {
          await request<Group>(path, { method: operation === "create" ? "POST" : "PATCH", body: { name: value } });
        }
        message.textContent = "Perubahan tersimpan. Memuat halaman...";
        const next = new URL(returnTo, location.origin);
        next.searchParams.delete("group");
        next.searchParams.set("notice", operation === "create" ? "created" : operation === "update" ? "updated" : "deleted");
        navigate(next.pathname + next.search);
      } catch (err) {
        if (err instanceof ApiRequestError && err.status === 401) {
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
      }
    });
    button.disabled = false;
  }
}
