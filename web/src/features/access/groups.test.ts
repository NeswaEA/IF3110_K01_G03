import { fireEvent, screen, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { readFileSync } from "node:fs";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setCsrfToken } from "../../api/client";
import { bindGroups } from "./groups";

const base = "http://localhost/api/v1";
const groupId = "20000000-0000-4000-8000-000000000001";
const returnTo = "/admin/groups?offset=50&group=" + groupId;
const template = readFileSync("../backend/internal/webui/templates/access/groups.gohtml", "utf8");
const server = setupServer();
let root: HTMLElement;
let navigate = vi.fn<(url: string) => void>();
let csrfRequests = 0;

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
beforeEach(() => {
  setCsrfToken(null);
  csrfRequests = 0;
  document.body.innerHTML = template
    .replaceAll("{{.Data.ReturnTo}}", returnTo.replaceAll("&", "&amp;"))
    .replaceAll("{{.Data.Selected.ID}}", groupId)
    .replaceAll("{{.Data.Selected.Name}}", "existing-group")
    .replace(/{{[^}]*}}/g, "");
  root = document.querySelector<HTMLElement>(".groups-admin")!;
  navigate = vi.fn();
  server.use(http.get(`${base}/auth/csrf`, () => {
    csrfRequests++;
    return HttpResponse.json({ data: { csrf_token: `csrf-${csrfRequests}` } });
  }));
  bindGroups(root, navigate);
});
afterEach(() => {
  server.resetHandlers();
  document.body.innerHTML = "";
});
afterAll(() => server.close());

async function fillCreate(value = "New Group") {
  const user = userEvent.setup();
  await user.type(document.querySelector<HTMLInputElement>("#new-group-name")!, value);
  return user;
}

function apiError(status: number, code: string, message: string, fields = {}) {
  return HttpResponse.json({ error: { code, message, fields, request_id: "req-test" } }, { status });
}

describe("group administration forms", () => {
  it("creates a group with trimmed case-sensitive JSON and lazy CSRF", async () => {
    let body: unknown;
    server.use(http.post(`${base}/admin/groups`, async ({ request }) => {
      expect(request.headers.get("Content-Type")).toBe("application/json");
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-1");
      body = await request.json();
      return HttpResponse.json({ data: {} }, { status: 201 });
    }));
    const user = await fillCreate("  New Group  ");
    expect(csrfRequests).toBe(0);
    await user.click(screen.getByRole("button", { name: "Buat grup" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/admin/groups?offset=50&notice=created"));
    expect(body).toEqual({ name: "New Group" });
    expect(csrfRequests).toBe(1);
  });

  it("renames with PATCH and drops the selection because sorting may change", async () => {
    let body: unknown;
    server.use(http.patch(`${base}/admin/groups/${groupId}`, async ({ request }) => {
      body = await request.json();
      return HttpResponse.json({ data: {} });
    }));
    const user = userEvent.setup();
    const name = document.querySelector<HTMLInputElement>("#update-group-name")!;
    await user.clear(name);
    await user.type(name, "Renamed Group");
    await user.click(screen.getByRole("button", { name: "Simpan nama grup" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/admin/groups?offset=50&notice=updated"));
    expect(body).toEqual({ name: "Renamed Group" });
  });

  it.each([
    ["\u0085Group\u0085", "Group"],
    ["\uFEFFGroup\uFEFF", "\uFEFFGroup\uFEFF"],
  ])("uses the same Unicode whitespace rules as the server", async (input, name) => {
    server.use(http.post(`${base}/admin/groups`, async ({ request }) => {
      expect(await request.json()).toEqual({ name });
      return HttpResponse.json({ data: {} }, { status: 201 });
    }));
    document.querySelector<HTMLInputElement>("#new-group-name")!.value = input;
    fireEvent.submit(document.querySelector("#create-group-form")!);
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  });

  it("requires confirmation, deletes with no body, and accepts an empty 204", async () => {
    server.use(http.delete(`${base}/admin/groups/${groupId}`, async ({ request }) => {
      expect(await request.text()).toBe("");
      expect(request.headers.get("Content-Type")).toBeNull();
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-1");
      return new HttpResponse(null, { status: 204 });
    }));
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Hapus grup" }));
    expect(csrfRequests).toBe(0);
    expect(navigate).not.toHaveBeenCalled();
    await user.click(screen.getByLabelText("Saya ingin menghapus grup ini."));
    await user.click(screen.getByRole("button", { name: "Hapus grup" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/admin/groups?offset=50&notice=deleted"));
  });

  it.each(["   ", "x".repeat(101), "bad\0name"])("rejects an invalid group name before sending a request", async (value) => {
    const input = document.querySelector<HTMLInputElement>("#new-group-name")!;
    input.value = value;
    fireEvent.submit(document.querySelector("#create-group-form")!);
    expect(document.querySelector("#create-group-message")).toHaveTextContent("Nama grup harus berisi 1 sampai 100 karakter tanpa karakter NUL.");
    expect(input).toHaveFocus();
    expect(csrfRequests).toBe(0);
  });

  it("supports 100 Unicode code points and keyboard submission", async () => {
    server.use(http.post(`${base}/admin/groups`, () => HttpResponse.json({ data: {} }, { status: 201 })));
    const user = await fillCreate("😀".repeat(100));
    await user.keyboard("{Enter}");
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  });

  it("locks every form and prevents duplicate mutations while pending", async () => {
    let finish!: () => void;
    let attempts = 0;
    const pending = new Promise<void>((resolve) => { finish = resolve; });
    server.use(http.post(`${base}/admin/groups`, async () => {
      attempts++;
      await pending;
      return HttpResponse.json({ data: {} }, { status: 201 });
    }));
    const user = await fillCreate();
    try {
      await user.click(screen.getByRole("button", { name: "Buat grup" }));
      await waitFor(() => expect(attempts).toBe(1));
      expect(root).toHaveAttribute("aria-busy", "true");
      for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
      fireEvent.submit(document.querySelector("#create-group-form")!);
      fireEvent.submit(document.querySelector("#update-group-form")!);
      expect(attempts).toBe(1);
      expect(document.querySelector("#create-group-message")).toHaveTextContent("Menyimpan grup...");
    } finally {
      finish();
    }
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  });

  it.each([
    [409, "ALREADY_EXISTS", "Nama grup sudah digunakan."],
    [403, "FORBIDDEN", "Akses ditolak."],
    [404, "NOT_FOUND", "Grup tidak ditemukan."],
    [500, "INTERNAL_ERROR", "Perubahan gagal disimpan."],
  ])("shows %s errors and restores the forms without automatic retry", async (status, code, message) => {
    server.use(http.patch(`${base}/admin/groups/${groupId}`, () => apiError(status, code, message)));
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Simpan nama grup" }));
    await waitFor(() => expect(document.querySelector("#update-group-message")).toHaveTextContent(message));
    expect(screen.getByRole("button", { name: "Simpan nama grup" })).toBeEnabled();
    expect(root).not.toHaveAttribute("aria-busy");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("renders error fields as text and keeps the entered group name", async () => {
    server.use(http.post(`${base}/admin/groups`, () => apiError(400, "INVALID_REQUEST", "Data grup tidak valid.", {
      name: "<img src=x onerror=alert(1)>",
    })));
    const user = await fillCreate();
    await user.click(screen.getByRole("button", { name: "Buat grup" }));
    await waitFor(() => expect(document.querySelector("#create-group-message")).toHaveTextContent("<img src=x onerror=alert(1)>"));
    expect(document.querySelector("img")).toBeNull();
    expect(document.querySelector("#new-group-name")).toHaveValue("New Group");
  });

  it("redirects an expired session to login without replaying deletion", async () => {
    let attempts = 0;
    server.use(http.delete(`${base}/admin/groups/${groupId}`, () => {
      attempts++;
      return apiError(401, "UNAUTHENTICATED", "Session expired");
    }));
    const user = userEvent.setup();
    await user.click(screen.getByLabelText("Saya ingin menghapus grup ini."));
    await user.click(screen.getByRole("button", { name: "Hapus grup" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/login?next=" + encodeURIComponent(returnTo)));
    expect(attempts).toBe(1);
    expect(navigate).toHaveBeenCalledTimes(1);
  });

  it("fetches a fresh CSRF token only after manual retry", async () => {
    let attempts = 0;
    server.use(http.post(`${base}/admin/groups`, ({ request }) => {
      if (++attempts === 1) return apiError(403, "CSRF_REJECTED", "Expired");
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-2");
      return HttpResponse.json({ data: {} }, { status: 201 });
    }));
    const user = await fillCreate();
    await user.click(screen.getByRole("button", { name: "Buat grup" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Buat grup" })).toBeEnabled());
    expect(document.querySelector("#create-group-message")).toHaveTextContent("Token CSRF kedaluwarsa.");
    expect(attempts).toBe(1);
    await user.click(screen.getByRole("button", { name: "Buat grup" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    expect(csrfRequests).toBe(2);
  });

  it("reports a network failure and leaves manual retry available", async () => {
    server.use(http.get(`${base}/auth/csrf`, () => HttpResponse.error()));
    const user = await fillCreate();
    await user.click(screen.getByRole("button", { name: "Buat grup" }));
    await waitFor(() => expect(document.querySelector("#create-group-message")).toHaveTextContent("Tidak dapat menghubungi server."));
    expect(screen.getByRole("button", { name: "Buat grup" })).toBeEnabled();
    expect(navigate).not.toHaveBeenCalled();
  });
});
