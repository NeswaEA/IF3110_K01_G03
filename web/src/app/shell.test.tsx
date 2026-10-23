import { screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { act } from "react";
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { request, setCsrfToken } from "../api/client";
import { bindLogout } from "./logout";
import { mountWidget, showSessionExpired } from "./mount";

// mountWidget renders with a real root rather than Testing Library's render, so opt in to act().
(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

const B = "http://localhost/api/v1";
const server = setupServer();
beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
beforeEach(() => {
  setCsrfToken("t");
  history.replaceState(null, "", "/databases/abc/migrations?draft=1");
  document.body.innerHTML = "<main><h1>Migrasi</h1></main>";
});
afterEach(() => server.resetHandlers());
afterAll(() => server.close());

const unauthorized = () =>
  HttpResponse.json({ error: { code: "UNAUTHENTICATED", message: "Silakan login.", fields: {}, request_id: "r" } }, { status: 401 });

describe("mountWidget", () => {
  it("renders into the element the server named and passes its data attributes", async () => {
    document.querySelector("main")!.insertAdjacentHTML("beforeend", '<section data-widget="demo" data-database-id="db-1"></section>');
    await act(async () => mountWidget("demo", (el) => <p>widget {el.dataset.databaseId}</p>, "notify"));
    expect(screen.getByText("widget db-1")).toBeInTheDocument();
  });

  it("does nothing when the page has no such widget", () => {
    expect(() => mountWidget("absent", () => <p>never</p>)).not.toThrow();
    expect(screen.queryByText("never")).not.toBeInTheDocument();
  });

  it("keeps the page on 401 in notify mode and offers login in a new tab", async () => {
    document.querySelector("main")!.insertAdjacentHTML("beforeend", '<section data-widget="editor"></section>');
    await act(async () => mountWidget("editor", () => <textarea defaultValue="SELECT 1" aria-label="SQL" />, "notify"));
    server.use(http.get(`${B}/probe`, unauthorized));
    await expect(request("/probe")).rejects.toMatchObject({ status: 401 });
    const link = screen.getByRole("link", { name: "Masuk lagi di tab baru" });
    expect(link).toHaveAttribute("href", "/login?next=%2Fdatabases%2Fabc%2Fmigrations%3Fdraft%3D1");
    expect(link).toHaveAttribute("target", "_blank");
    expect(screen.getByRole("textbox", { name: "SQL" })).toHaveValue("SELECT 1");
    showSessionExpired();
    expect(screen.getAllByRole("alert")).toHaveLength(1);
  });
});

describe("bindLogout", () => {
  function button() {
    document.body.insertAdjacentHTML("beforeend", '<button type="button">Keluar</button>');
    const b = screen.getByRole("button", { name: "Keluar" }) as HTMLButtonElement;
    const navigate = vi.fn();
    bindLogout(b, navigate);
    return { b, navigate };
  }

  it("revokes the session with CSRF, then opens the login page", async () => {
    let csrf: string | null = null;
    server.use(http.post(`${B}/auth/logout`, ({ request }) => {
      csrf = request.headers.get("X-CSRF-Token");
      return new HttpResponse(null, { status: 204 });
    }));
    const { b, navigate } = button();
    await userEvent.click(b);
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/login"));
    expect(csrf).toBe("t");
  });

  it("treats an already expired session as logged out", async () => {
    server.use(http.post(`${B}/auth/logout`, unauthorized));
    const { b, navigate } = button();
    await userEvent.click(b);
    await waitFor(() => expect(navigate).toHaveBeenCalledWith("/login"));
  });

  it("stays on the page and explains a failed logout", async () => {
    server.use(http.post(`${B}/auth/logout`, () =>
      HttpResponse.json({ error: { code: "INTERNAL_ERROR", message: "Server bermasalah.", fields: {}, request_id: "r" } }, { status: 500 }),
    ));
    const { b, navigate } = button();
    await userEvent.click(b);
    expect(await screen.findByRole("alert")).toHaveTextContent("Gagal keluar: Server bermasalah.");
    expect(b).toBeEnabled();
    expect(navigate).not.toHaveBeenCalled();
  });
});
