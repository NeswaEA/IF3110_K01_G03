import { fireEvent, screen, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { readFileSync } from "node:fs";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { setCsrfToken } from "../../api/client";
import { bindGrants } from "./grants";

const base = "http://localhost/api/v1";
const groupId = "20000000-0000-4000-8000-000000000001";
const databaseId = "30000000-0000-4000-8000-000000000001";
const page = "/admin/groups/" + groupId + "/grants";
const path = "/admin/groups/" + groupId + "/grants/" + databaseId;
const returnTo = page + "?offset=50&databases_offset=100&database=" + databaseId;
const template = readFileSync("../backend/internal/webui/templates/access/grants.gohtml", "utf8");
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
    .replaceAll("{{.Data.Selected.Database.ID}}", databaseId)
    .replaceAll("{{.ID}}", databaseId)
    .replaceAll("{{.Name}}", "demo")
    .replaceAll("{{.Data.Selected.Database.Name}}", "demo")
    .replace(/{{[^}]*}}/g, "");
  root = document.querySelector<HTMLElement>(".grants-admin")!;
  navigate = vi.fn();
  server.use(http.get(`${base}/auth/csrf`, () => {
    csrfRequests++;
    return HttpResponse.json({ data: { csrf_token: `csrf-${csrfRequests}` } });
  }));
  bindGrants(root, navigate);
});
afterEach(() => {
  server.resetHandlers();
  document.body.innerHTML = "";
});
afterAll(() => server.close());

function apiError(status: number, code: string, message: string, fields = {}) {
  return HttpResponse.json({ error: { code, message, fields, request_id: "req-test" } }, { status });
}

describe("grant forms", () => {
  it.each(["view", "edit"])("sets %s access with PUT and lazy CSRF", async (level) => {
    server.use(http.put(base + path, async ({ request }) => {
      expect(request.headers.get("Content-Type")).toBe("application/json");
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-1");
      expect(await request.json()).toEqual({ level });
      return HttpResponse.json({ data: { database_id: databaseId, level } });
    }));
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Database"), databaseId);
    await user.selectOptions(screen.getByLabelText("Hak akses", { exact: true }), level);
    expect(csrfRequests).toBe(0);
    await user.click(screen.getByRole("button", { name: "Tetapkan hak akses" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(page + "?offset=50&databases_offset=100&notice=saved"));
    expect(csrfRequests).toBe(1);
  });

  it("requires a database selection before sending a request", async () => {
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Tetapkan hak akses" }));
    fireEvent.submit(document.querySelector("#set-grant-form")!);
    expect(csrfRequests).toBe(0);
    expect(navigate).not.toHaveBeenCalled();
  });

  it("updates the selected grant and supports keyboard submission", async () => {
    server.use(http.put(base + path, async ({ request }) => {
      expect(await request.json()).toEqual({ level: "view" });
      return HttpResponse.json({ data: { database_id: databaseId, level: "view" } });
    }));
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Hak akses baru"), "view");
    screen.getByRole("button", { name: "Simpan hak akses" }).focus();
    await user.keyboard("{Enter}");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(page + "?offset=50&databases_offset=100&notice=saved"));
  });

  it("requires confirmation and removes with DELETE without a body", async () => {
    server.use(http.delete(base + path, async ({ request }) => {
      expect(await request.text()).toBe("");
      expect(request.headers.get("Content-Type")).toBeNull();
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-1");
      return new HttpResponse(null, { status: 204 });
    }));
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Cabut hak akses" }));
    expect(csrfRequests).toBe(0);
    const confirmation = screen.getByRole("checkbox");
    confirmation.focus();
    await user.keyboard(" ");
    expect(confirmation).toBeChecked();
    await user.click(screen.getByRole("button", { name: "Cabut hak akses" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith(page + "?offset=50&databases_offset=100&notice=removed"));
  });

  it("locks all forms and prevents duplicate or concurrent mutations", async () => {
    let finish!: () => void;
    let attempts = 0;
    const pending = new Promise<void>((resolve) => { finish = resolve; });
    server.use(http.put(base + path, async () => {
      attempts++;
      await pending;
      return HttpResponse.json({ data: { database_id: databaseId, level: "edit" } });
    }));
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Database"), databaseId);
    try {
      await user.click(screen.getByRole("button", { name: "Tetapkan hak akses" }));
      await waitFor(() => expect(attempts).toBe(1));
      expect(root).toHaveAttribute("aria-busy", "true");
      for (const button of screen.getAllByRole("button")) expect(button).toBeDisabled();
      for (const select of screen.getAllByRole("combobox")) expect(select).toBeDisabled();
      for (const form of root.querySelectorAll("form")) fireEvent.submit(form);
      expect(attempts).toBe(1);
      expect(document.querySelector("#set-grant-message")).toHaveTextContent("Menyimpan hak akses...");
    } finally {
      finish();
    }
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  });

  it.each([
    [403, "FORBIDDEN", "Akses ditolak."],
    [404, "NOT_FOUND", "Grup atau database tidak ditemukan."],
    [500, "INTERNAL_ERROR", "Perubahan gagal disimpan."],
  ])("shows %s errors and retains the selected database and access level", async (status, code, message) => {
    server.use(http.put(base + path, () => apiError(status, code, message)));
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Database"), databaseId);
    await user.selectOptions(screen.getByLabelText("Hak akses", { exact: true }), "edit");
    await user.click(screen.getByRole("button", { name: "Tetapkan hak akses" }));
    await waitFor(() => expect(document.querySelector("#set-grant-message")).toHaveTextContent(message));
    expect(screen.getByRole("button", { name: "Tetapkan hak akses" })).toBeEnabled();
    expect(screen.getByLabelText("Database")).toHaveValue(databaseId);
    expect(screen.getByLabelText("Hak akses", { exact: true })).toHaveValue("edit");
    expect(root).not.toHaveAttribute("aria-busy");
    expect(navigate).not.toHaveBeenCalled();
  });

  it("shows removal errors without assuming the grant changed", async () => {
    server.use(http.delete(base + path, () => apiError(500, "INTERNAL_ERROR", "Hak akses gagal dicabut.")));
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "Cabut hak akses" }));
    await waitFor(() => expect(document.querySelector("#remove-grant-message")).toHaveTextContent("Hak akses gagal dicabut."));
    expect(screen.getByRole("button", { name: "Cabut hak akses" })).toBeEnabled();
    expect(navigate).not.toHaveBeenCalled();
  });

  it("renders validation fields as text", async () => {
    server.use(http.put(base + path, () => apiError(400, "INVALID_REQUEST", "Data tidak valid.", {
      level: "<img src=x onerror=alert(1)>",
    })));
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Hak akses baru"), "edit");
    await user.click(screen.getByRole("button", { name: "Simpan hak akses" }));
    await waitFor(() => expect(document.querySelector("#update-grant-message")).toHaveTextContent("<img src=x onerror=alert(1)>"));
    expect(document.querySelector("img")).toBeNull();
  });

  it("redirects an expired session without replaying the mutation", async () => {
    let attempts = 0;
    server.use(http.delete(base + path, () => {
      attempts++;
      return apiError(401, "UNAUTHENTICATED", "Session expired");
    }));
    const user = userEvent.setup();
    await user.click(screen.getByRole("checkbox"));
    await user.click(screen.getByRole("button", { name: "Cabut hak akses" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/login?next=" + encodeURIComponent(returnTo)));
    expect(attempts).toBe(1);
    expect(navigate).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("button", { name: "Cabut hak akses" })).toBeDisabled();
  });

  it("fetches a fresh CSRF token only on manual retry after rejection", async () => {
    let attempts = 0;
    server.use(http.put(base + path, ({ request }) => {
      if (++attempts === 1) return apiError(403, "CSRF_REJECTED", "Expired");
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-2");
      return HttpResponse.json({ data: { database_id: databaseId, level: "view" } });
    }));
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Database"), databaseId);
    await user.click(screen.getByRole("button", { name: "Tetapkan hak akses" }));
    await waitFor(() => expect(screen.getByRole("button", { name: "Tetapkan hak akses" })).toBeEnabled());
    expect(document.querySelector("#set-grant-message")).toHaveTextContent("Token CSRF kedaluwarsa.");
    expect(attempts).toBe(1);
    await user.click(screen.getByRole("button", { name: "Tetapkan hak akses" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    expect(csrfRequests).toBe(2);
  });

  it("reports a network failure and retains the selected access level", async () => {
    server.use(http.get(`${base}/auth/csrf`, () => HttpResponse.error()));
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Hak akses baru"), "edit");
    await user.click(screen.getByRole("button", { name: "Simpan hak akses" }));
    await waitFor(() => expect(document.querySelector("#update-grant-message")).toHaveTextContent("Tidak dapat menghubungi server."));
    expect(screen.getByLabelText("Hak akses baru")).toHaveValue("edit");
    expect(screen.getByRole("button", { name: "Simpan hak akses" })).toBeEnabled();
  });
});
