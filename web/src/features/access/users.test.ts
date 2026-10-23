import { fireEvent, screen, waitFor, within } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { readFileSync } from "node:fs";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setCsrfToken } from "../../api/client";
import { bindUsers } from "./users";
import lastAdminError from "../../../../tests/fixtures/api/access_error_last_admin.json";

const base = "http://localhost/api/v1";
const accountId = "10000000-0000-4000-8000-000000000002";
const returnTo = "/admin/users?offset=50&user=" + accountId;
const password = "example password 123";
const template = readFileSync("../backend/internal/webui/templates/access/users.gohtml", "utf8");
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
    .replaceAll("{{.Data.Selected.ID}}", accountId)
    .replace(/{{[^}]*}}/g, "");
  root = document.querySelector<HTMLElement>(".users-admin")!;
  navigate = vi.fn();
  server.use(http.get(`${base}/auth/csrf`, () => {
    csrfRequests++;
    return HttpResponse.json({ data: { csrf_token: `csrf-${csrfRequests}` } });
  }));
  bindUsers(root, navigate);
});
afterEach(() => {
  server.resetHandlers();
  document.body.innerHTML = "";
});
afterAll(() => server.close());

async function fillCreate(value = password) {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Username"), "new-user");
  await user.type(screen.getByLabelText("Password awal"), value);
  return user;
}

function apiError(status: number, code: string, message: string, fields = {}) {
  return HttpResponse.json({ error: { code, message, fields, request_id: "req-test" } }, { status });
}

describe("user administration forms", () => {
  it("creates a user with JSON and lazy CSRF, then navigates to the refreshed list", async () => {
    let body: unknown;
    server.use(http.post(`${base}/admin/users`, async ({ request }) => {
      expect(request.headers.get("Content-Type")).toBe("application/json");
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-1");
      body = await request.json();
      return HttpResponse.json({ data: {} }, { status: 201 });
    }));
    const user = await fillCreate();
    const form = document.querySelector<HTMLFormElement>("#create-user-form")!;
    await user.click(within(form).getByLabelText("Admin platform"));
    expect(csrfRequests).toBe(0);
    await user.click(screen.getByRole("button", { name: "Buat pengguna" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/admin/users?offset=50&notice=created"));
    expect(body).toEqual({ username: "new-user", password, is_platform_admin: true });
    expect(screen.getByLabelText("Password awal")).toHaveValue("");
    expect(csrfRequests).toBe(1);
  });

  it("updates active and admin flags with PATCH", async () => {
    let body: unknown;
    server.use(http.patch(`${base}/admin/users/${accountId}`, async ({ request }) => {
      body = await request.json();
      return HttpResponse.json({ data: {} });
    }));
    const user = userEvent.setup();
    const form = document.querySelector<HTMLFormElement>("#update-user-form")!;
    await user.click(within(form).getByLabelText("Akun aktif"));
    await user.click(within(form).getByLabelText("Admin platform"));
    await user.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(returnTo + "&notice=updated"));
    expect(body).toEqual({ active: false, is_platform_admin: false });
  });

  it("resets a password with PUT, accepts 204, and clears the input", async () => {
    let body: unknown;
    server.use(http.put(`${base}/admin/users/${accountId}/password`, async ({ request }) => {
      body = await request.json();
      return new HttpResponse(null, { status: 204 });
    }));
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Password baru"), password);
    await user.click(screen.getByRole("button", { name: "Ganti password" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(returnTo + "&notice=password"));
    expect(body).toEqual({ password });
    expect(screen.getByLabelText("Password baru")).toHaveValue("");
  });

  it("validates required fields and password length before making requests", async () => {
    fireEvent.submit(document.querySelector("#create-user-form")!);
    expect(csrfRequests).toBe(0);
    const user = await fillCreate("short");
    await user.click(screen.getByRole("button", { name: "Buat pengguna" }));
    expect(document.querySelector("#create-user-message")).toHaveTextContent("Password harus berisi 12 sampai 128 karakter.");
    expect(screen.getByLabelText("Password awal")).toHaveFocus();
    expect(csrfRequests).toBe(0);
    expect(navigate).not.toHaveBeenCalled();
  });

  it("counts password characters as Unicode code points", async () => {
    server.use(http.post(`${base}/admin/users`, () => HttpResponse.json({ data: {} }, { status: 201 })));
    const user = await fillCreate("😀".repeat(128));
    await user.click(screen.getByRole("button", { name: "Buat pengguna" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  });

  it("locks all forms and suppresses duplicate submissions during a mutation", async () => {
    let finish!: () => void;
    let attempts = 0;
    const pending = new Promise<void>((resolve) => { finish = resolve; });
    server.use(http.post(`${base}/admin/users`, async () => {
      attempts++;
      await pending;
      return HttpResponse.json({ data: {} }, { status: 201 });
    }));
    const user = await fillCreate();
    try {
      await user.click(screen.getByRole("button", { name: "Buat pengguna" }));
      await waitFor(() => expect(attempts).toBe(1));
      expect(root).toHaveAttribute("aria-busy", "true");
      for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
      fireEvent.submit(document.querySelector("#create-user-form")!);
      fireEvent.submit(document.querySelector("#update-user-form")!);
      expect(attempts).toBe(1);
      expect(document.querySelector("#create-user-message")).toHaveTextContent("Menyimpan pengguna...");
    } finally {
      finish();
    }
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  });

  it.each([
    [409, lastAdminError.error.code, lastAdminError.error.message],
    [403, "FORBIDDEN", "Akses ditolak."],
    [429, "RATE_LIMITED", "Pemrosesan password sedang penuh. Coba lagi."],
  ])("shows %s errors and leaves manual retry available", async (status, code, message) => {
    server.use(http.patch(`${base}/admin/users/${accountId}`, () => code === lastAdminError.error.code
      ? HttpResponse.json(lastAdminError, { status })
      : apiError(status, code, message)));
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    await waitFor(() => expect(document.querySelector("#update-user-message")).toHaveTextContent(message));
    expect(screen.getByRole("button", { name: "Simpan perubahan" })).toBeEnabled();
    expect(root).not.toHaveAttribute("aria-busy");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("shows field validation as text without interpreting HTML", async () => {
    server.use(http.post(`${base}/admin/users`, () => apiError(409, "ALREADY_EXISTS", "Username sudah digunakan.", {
      username: "<img src=x onerror=alert(1)>",
    })));
    const user = await fillCreate();
    await user.click(screen.getByRole("button", { name: "Buat pengguna" }));
    await waitFor(() => expect(document.querySelector("#create-user-message")).toHaveTextContent("<img src=x onerror=alert(1)>"));
    expect(document.querySelector("img")).toBeNull();
    expect(screen.getByLabelText("Username")).toHaveValue("new-user");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("opens login on 401 and clears the password without resubmitting", async () => {
    server.use(http.put(`${base}/admin/users/${accountId}/password`, () => apiError(401, "UNAUTHENTICATED", "Session expired")));
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Password baru"), password);
    await user.click(screen.getByRole("button", { name: "Ganti password" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/login?next=" + encodeURIComponent(returnTo)));
    expect(screen.getByLabelText("Password baru")).toHaveValue("");
    expect(navigate).toHaveBeenCalledTimes(1);
  });

  it("refreshes CSRF on a manual retry after CSRF rejection", async () => {
    let attempts = 0;
    server.use(http.post(`${base}/admin/users`, ({ request }) => {
      if (++attempts === 1) return apiError(403, "CSRF_REJECTED", "Expired");
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-2");
      return HttpResponse.json({ data: {} }, { status: 201 });
    }));
    const user = await fillCreate();
    await user.click(screen.getByRole("button", { name: "Buat pengguna" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Buat pengguna" })).toBeEnabled());
    expect(attempts).toBe(1);
    expect(navigate).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Buat pengguna" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    expect(csrfRequests).toBe(2);
  });

  it("handles a network failure without an automatic retry", async () => {
    server.use(http.get(`${base}/auth/csrf`, () => HttpResponse.error()));
    const user = await fillCreate();
    await user.click(screen.getByRole("button", { name: "Buat pengguna" }));
    await waitFor(() => expect(document.querySelector("#create-user-message")).toHaveTextContent("Tidak dapat menghubungi server."));
    expect(screen.getByRole("button", { name: "Buat pengguna" })).toBeEnabled();
    expect(navigate).not.toHaveBeenCalled();
  });
});
