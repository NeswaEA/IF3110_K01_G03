import { fireEvent, screen, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { readFileSync } from "node:fs";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setCsrfToken } from "../../api/client";
import { bindMembers } from "./members";

const base = "http://localhost/api/v1";
const groupId = "20000000-0000-4000-8000-000000000001";
const userId = "10000000-0000-4000-8000-000000000002";
const page = "/admin/groups/" + groupId + "/members";
const path = "/admin/groups/" + groupId + "/members/" + userId;
const returnTo = page + "?offset=50&users_offset=100&member=" + userId;
const template = readFileSync("../backend/internal/webui/templates/access/members.gohtml", "utf8");
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
    .replaceAll("{{.Data.Group.ID}}", groupId)
    .replaceAll("{{.Data.Selected.ID}}", userId)
    .replaceAll("{{.ID}}", userId)
    .replaceAll("{{.Username}}", "member-user")
    .replaceAll("{{.Data.Selected.Username}}", "member-user")
    .replace(/{{[^}]*}}/g, "");
  root = document.querySelector<HTMLElement>(".members-admin")!;
  navigate = vi.fn();
  server.use(http.get(`${base}/auth/csrf`, () => {
    csrfRequests++;
    return HttpResponse.json({ data: { csrf_token: `csrf-${csrfRequests}` } });
  }));
  bindMembers(root, navigate);
});
afterEach(() => {
  server.resetHandlers();
  document.body.innerHTML = "";
});
afterAll(() => server.close());

function apiError(status: number, code: string, message: string, fields = {}) {
  return HttpResponse.json({ error: { code, message, fields, request_id: "req-test" } }, { status });
}

async function chooseUser() {
  const user = userEvent.setup();
  await user.selectOptions(screen.getByLabelText("Pengguna"), userId);
  return user;
}

describe("membership forms", () => {
  it("adds with PUT, an empty JSON object, and lazy CSRF, then reloads both lists", async () => {
    server.use(http.put(base + path, async ({ request }) => {
      expect(request.headers.get("Content-Type")).toBe("application/json");
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-1");
      expect(await request.json()).toEqual({});
      return new HttpResponse(null, { status: 204 });
    }));
    const user = await chooseUser();
    expect(csrfRequests).toBe(0);
    await user.click(screen.getByRole("button", { name: "Tambah anggota" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(page + "?offset=50&users_offset=100&notice=added"));
    expect(csrfRequests).toBe(1);
  });

  it("requires a user selection before sending a request", async () => {
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Tambah anggota" }));
    fireEvent.submit(document.querySelector("#add-member-form")!);
    expect(csrfRequests).toBe(0);
    expect(navigate).not.toHaveBeenCalled();
  });

  it("requires confirmation and removes with DELETE without a body", async () => {
    server.use(http.delete(base + path, async ({ request }) => {
      expect(await request.text()).toBe("");
      expect(request.headers.get("Content-Type")).toBeNull();
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-1");
      return new HttpResponse(null, { status: 204 });
    }));
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Keluarkan anggota" }));
    expect(csrfRequests).toBe(0);
    const confirmation = screen.getByRole("checkbox");
    confirmation.focus();
    await user.keyboard(" ");
    expect(confirmation).toBeChecked();
    screen.getByRole("button", { name: "Keluarkan anggota" }).focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(page + "?offset=50&users_offset=100&notice=removed"));
  });

  it("locks all forms and prevents duplicate or concurrent mutations", async () => {
    let finish!: () => void;
    let attempts = 0;
    const pending = new Promise<void>((resolve) => { finish = resolve; });
    server.use(http.put(base + path, async () => {
      attempts++;
      await pending;
      return new HttpResponse(null, { status: 204 });
    }));
    const user = await chooseUser();
    try {
      await user.click(screen.getByRole("button", { name: "Tambah anggota" }));
      await waitFor(() => expect(attempts).toBe(1));
      expect(root).toHaveAttribute("aria-busy", "true");
      expect(screen.getByLabelText("Pengguna")).toBeDisabled();
      for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
      fireEvent.submit(document.querySelector("#add-member-form")!);
      fireEvent.submit(document.querySelector("#remove-member-form")!);
      expect(attempts).toBe(1);
      expect(document.querySelector("#add-member-message")).toHaveTextContent("Menambahkan anggota...");
    } finally {
      finish();
    }
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  });

  it.each([
    [403, "FORBIDDEN", "Akses ditolak."],
    [404, "NOT_FOUND", "Grup atau pengguna tidak ditemukan."],
    [500, "INTERNAL_ERROR", "Perubahan gagal disimpan."],
  ])("shows %s errors and restores manual retry", async (status, code, message) => {
    server.use(http.put(base + path, () => apiError(status, code, message)));
    const user = await chooseUser();
    await user.click(screen.getByRole("button", { name: "Tambah anggota" }));
    await waitFor(() => expect(document.querySelector("#add-member-message")).toHaveTextContent(message));
    expect(screen.getByRole("button", { name: "Tambah anggota" })).toBeEnabled();
    expect(screen.getByLabelText("Pengguna")).toHaveValue(userId);
    expect(root).not.toHaveAttribute("aria-busy");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("shows removal errors without assuming the membership changed", async () => {
    server.use(http.delete(base + path, () => apiError(500, "INTERNAL_ERROR", "Keanggotaan gagal dihapus.")));
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "Keluarkan anggota" }));
    await waitFor(() => expect(document.querySelector("#remove-member-message")).toHaveTextContent("Keanggotaan gagal dihapus."));
    expect(screen.getByRole("button", { name: "Keluarkan anggota" })).toBeEnabled();
    expect(navigate).not.toHaveBeenCalled();
  });

  it("renders validation fields as text", async () => {
    server.use(http.put(base + path, () => apiError(400, "INVALID_REQUEST", "Data tidak valid.", {
      userId: "<img src=x onerror=alert(1)>",
    })));
    const user = await chooseUser();
    await user.click(screen.getByRole("button", { name: "Tambah anggota" }));
    await waitFor(() => expect(document.querySelector("#add-member-message")).toHaveTextContent("<img src=x onerror=alert(1)>"));
    expect(document.querySelector("img")).toBeNull();
  });

  it("redirects an expired session without automatically replaying removal", async () => {
    let attempts = 0;
    server.use(http.delete(base + path, () => {
      attempts++;
      return apiError(401, "UNAUTHENTICATED", "Session expired");
    }));
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "Keluarkan anggota" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/login?next=" + encodeURIComponent(returnTo)));
    expect(attempts).toBe(1);
    expect(navigate).toHaveBeenCalledTimes(1);
  });

  it("fetches a fresh CSRF token on manual retry after rejection", async () => {
    let attempts = 0;
    server.use(http.put(base + path, ({ request }) => {
      if (++attempts === 1) return apiError(403, "CSRF_REJECTED", "Expired");
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-2");
      return new HttpResponse(null, { status: 204 });
    }));
    const user = await chooseUser();
    await user.click(screen.getByRole("button", { name: "Tambah anggota" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Tambah anggota" })).toBeEnabled());
    expect(document.querySelector("#add-member-message")).toHaveTextContent("Token CSRF kedaluwarsa.");
    expect(attempts).toBe(1);
    await user.click(screen.getByRole("button", { name: "Tambah anggota" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    expect(csrfRequests).toBe(2);
  });

  it("reports a network failure and retains the chosen user", async () => {
    server.use(http.get(`${base}/auth/csrf`, () => HttpResponse.error()));
    const user = await chooseUser();
    await user.click(screen.getByRole("button", { name: "Tambah anggota" }));
    await waitFor(() => expect(document.querySelector("#add-member-message")).toHaveTextContent("Tidak dapat menghubungi server."));
    expect(screen.getByLabelText("Pengguna")).toHaveValue(userId);
    expect(screen.getByRole("button", { name: "Tambah anggota" })).toBeEnabled();
  });
});
