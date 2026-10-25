import { fireEvent, screen, waitFor } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { readFileSync } from "node:fs";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { request, setCsrfToken } from "../../api/client";
import { bindLogin } from "./login";

const B = "http://localhost/api/v1";
const loginTemplate = readFileSync("../backend/internal/webui/templates/auth/login.gohtml", "utf8")
  .split('{{define "content"}}')[1].split("{{end}}")[0];
const server = setupServer();
let csrfRequests = 0;
let form: HTMLFormElement;
let navigate = vi.fn<(url: string) => void>();

beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
beforeEach(() => {
  setCsrfToken(null);
  csrfRequests = 0;
  document.body.innerHTML = loginTemplate
    .replace("{{.Data.Next}}", "/databases/db-1?schema=main&amp;table=items");
  form = document.querySelector("form")!;
  navigate = vi.fn();
  server.use(http.get(`${B}/auth/csrf`, () => {
    csrfRequests++;
    return HttpResponse.json({ data: { csrf_token: `csrf-${csrfRequests}` } });
  }));
  bindLogin(form, navigate);
});
afterEach(() => {
  server.resetHandlers();
  document.body.innerHTML = "";
});
afterAll(() => server.close());

async function fillLogin() {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText("Username"), "editor");
  await user.type(screen.getByLabelText("Password"), "example password 123");
  return user;
}

describe("login form", () => {
  it("fetches CSRF only on submit, logs in with JSON, and discards the old CSRF token", async () => {
    let body: unknown;
    let csrf: string | null = null;
    let contentType: string | null = null;
    server.use(http.post(`${B}/auth/login`, async ({ request }) => {
      body = await request.json();
      csrf = request.headers.get("X-CSRF-Token");
      contentType = request.headers.get("Content-Type");
      return HttpResponse.json({ data: { user: { username: "editor" } } });
    }));
    const user = await fillLogin();
    expect(csrfRequests).toBe(0);
    await user.keyboard("{Enter}");
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/databases/db-1?schema=main&table=items"));
    expect(body).toEqual({ username: "editor", password: "example password 123" });
    expect(csrf).toBe("csrf-1");
    expect(contentType).toBe("application/json");
    expect(csrfRequests).toBe(1);
    expect(screen.getByLabelText("Password")).toHaveValue("");
    server.use(http.post(`${B}/probe`, ({ request }) => {
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-2");
      return new HttpResponse(null, { status: 204 });
    }));
    await request("/probe", { method: "POST", body: {} });
    expect(csrfRequests).toBe(2);
  });

  it("rejects empty input before making requests", () => {
    fireEvent.submit(form);
    expect(csrfRequests).toBe(0);
    expect(navigate).not.toHaveBeenCalled();
    expect(screen.getByRole("button", { name: "Masuk" })).toBeEnabled();
  });

  it("disables the form and suppresses duplicate submissions while login is pending", async () => {
    let finish!: () => void;
    const pending = new Promise<void>((resolve) => { finish = resolve; });
    let attempts = 0;
    server.use(http.post(`${B}/auth/login`, async () => {
      attempts++;
      await pending;
      return HttpResponse.json({ data: {} });
    }));
    const user = await fillLogin();
    try {
      await user.click(screen.getByRole("button", { name: "Masuk" }));
      await waitFor(() => expect(attempts).toBe(1));
      expect(screen.getByRole("button", { name: "Masuk" })).toBeDisabled();
      expect(screen.getByLabelText("Username")).toBeDisabled();
      expect(screen.getByRole("status")).toHaveTextContent("Memeriksa akun...");
      expect(form).toHaveAttribute("aria-busy", "true");
      fireEvent.submit(form);
      expect(attempts).toBe(1);
    } finally {
      finish();
    }
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
  });

  it.each([
    [401, "UNAUTHENTICATED", "Username atau password tidak sesuai, atau akun tidak aktif."],
    [403, "CSRF_REJECTED", "Sesi login kedaluwarsa. Silakan coba masuk lagi."],
  ])("allows a manual retry after %s with a fresh CSRF token", async (status, code, message) => {
    let attempts = 0;
    server.use(http.post(`${B}/auth/login`, ({ request }) => {
      attempts++;
      if (attempts === 1) {
        return HttpResponse.json({ error: { code, message: "Denied", fields: {}, request_id: "r" } }, { status });
      }
      expect(request.headers.get("X-CSRF-Token")).toBe("csrf-2");
      return HttpResponse.json({ data: {} });
    }));
    const user = await fillLogin();
    await user.click(screen.getByRole("button", { name: "Masuk" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent(message));
    expect(screen.getByRole("button", { name: "Masuk" })).toBeEnabled();
    expect(form).not.toHaveAttribute("aria-busy");
    expect(navigate).not.toHaveBeenCalled();
    expect(attempts).toBe(1);
    await user.click(screen.getByRole("button", { name: "Masuk" }));
    await waitFor(() => expect(navigate).toHaveBeenCalledTimes(1));
    expect(csrfRequests).toBe(2);
  });

  it("reports a network failure without navigating or automatically retrying", async () => {
    server.use(http.get(`${B}/auth/csrf`, () => HttpResponse.error()));
    const user = await fillLogin();
    await user.click(screen.getByRole("button", { name: "Masuk" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("Tidak dapat menghubungi server."));
    expect(screen.getByRole("button", { name: "Masuk" })).toBeEnabled();
    expect(navigate).not.toHaveBeenCalled();
  });

  it("renders server error text without interpreting HTML", async () => {
    server.use(http.post(`${B}/auth/login`, () => HttpResponse.json({
      error: { code: "INTERNAL_ERROR", message: "<img src=x onerror=alert(1)>", fields: {}, request_id: "r" },
    }, { status: 500 })));
    const user = await fillLogin();
    await user.click(screen.getByRole("button", { name: "Masuk" }));
    await waitFor(() => expect(screen.getByRole("status")).toHaveTextContent("<img src=x onerror=alert(1)>"));
    expect(document.querySelector("img")).toBeNull();
    expect(navigate).not.toHaveBeenCalled();
  });
});
