import { ApiRequestError, request } from "../../api/client";
import { markInvalid } from "../../components/formFeedback";

export function bindMembers(root: HTMLElement, navigate: (url: string) => void): void {
  const forms = [...root.querySelectorAll<HTMLFormElement>("form[data-operation]")];
  const groupId = root.dataset.groupId!;
  const returnTo = root.dataset.returnTo!;
  let pending = false;

  for (const form of forms) {
    const message = form.querySelector<HTMLElement>('[role="status"]')!;
    const button = form.querySelector<HTMLButtonElement>('button[type="submit"]')!;
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (pending || !form.reportValidity()) return;
      delete message.dataset.tone;
      markInvalid(form, []);
      const adding = form.dataset.operation === "add";
      const userId = adding ? form.querySelector<HTMLSelectElement>("select")!.value : form.dataset.userId!;
      const path = "/admin/groups/" + groupId + "/members/" + userId;
      pending = true;
      root.setAttribute("aria-busy", "true");
      for (const other of forms) other.querySelector<HTMLFieldSetElement>("fieldset")!.disabled = true;
      message.textContent = adding ? "Menambahkan anggota..." : "Mengeluarkan anggota...";

      try {
        await request(path, adding ? { method: "PUT", body: {} } : { method: "DELETE" });
        message.textContent = "Perubahan tersimpan. Memuat halaman...";
        const next = new URL(returnTo, location.origin);
        next.searchParams.delete("member");
        next.searchParams.set("notice", adding ? "added" : "removed");
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
        message.dataset.tone = "error";
        markInvalid(form, err instanceof ApiRequestError ? Object.keys(err.fields) : []);
      }
    });
    button.disabled = false;
  }
}
