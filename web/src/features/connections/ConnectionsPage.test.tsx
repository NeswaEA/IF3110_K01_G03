import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { setCsrfToken } from "../../api/client";
import { handlers } from "../../mocks/handlers";
import { ConnectionsPage } from "./ConnectionsPage";

const server = setupServer(...handlers);
let posted: unknown = null;
beforeAll(() => {
  server.listen({ onUnhandledFrame: "error" });
  setCsrfToken("t");
});
afterEach(() => {
  server.resetHandlers(...handlers);
  posted = null;
  vi.restoreAllMocks();
});
afterAll(() => server.close());

describe("ConnectionsPage", () => {
  it("lists registrations with text status and no secrets", async () => {
    render(<ConnectionsPage />);
    expect(await screen.findByText("PostgreSQL latihan")).toBeInTheDocument();
    expect(screen.getByText("Aktif, siap migrasi")).toBeInTheDocument();
    expect(screen.getByText("Aktif, belum diuji")).toBeInTheDocument();
    // the registry table (not the create form, which has a Password field) shows no credential
    expect(screen.getAllByRole("table")[0].textContent).not.toMatch(/password|has_credentials/i);
  });

  it("registers a SQLite target and reloads", async () => {
    server.use(
      http.post("*/api/v1/admin/connections", async ({ request }) => {
        posted = await request.json();
        return HttpResponse.json({ data: {} }, { status: 201 });
      }),
    );
    render(<ConnectionsPage />);
    await screen.findByText("PostgreSQL latihan");
    await userEvent.type(screen.getByLabelText("Nama"), "Latihan baru");
    await userEvent.selectOptions(screen.getByLabelText("Driver"), "sqlite");
    await userEvent.type(screen.getByLabelText(/Berkas SQLite/), "baru.sqlite");
    await userEvent.click(screen.getByRole("button", { name: "Daftarkan" }));
    await vi.waitFor(() => expect(posted).toEqual({ name: "Latihan baru", driver: "sqlite", config: { file: "baru.sqlite" } }));
  });

  it("shows field errors from INVALID_REQUEST next to the field", async () => {
    render(<ConnectionsPage />);
    await screen.findByText("PostgreSQL latihan");
    await userEvent.type(screen.getByLabelText("Nama"), "x");
    await userEvent.type(screen.getByLabelText("Host"), "h");
    await userEvent.type(screen.getByLabelText("Port"), "70000");
    await userEvent.type(screen.getByLabelText("Nama database"), "d");
    await userEvent.type(screen.getByLabelText("Username"), "u");
    await userEvent.type(screen.getByLabelText("Password"), "p");
    await userEvent.type(screen.getByLabelText("Role editor terbatas"), "r");
    await userEvent.click(screen.getByRole("button", { name: "Daftarkan" }));
    expect(await screen.findByText("harus antara 1 dan 65535")).toBeInTheDocument();
  });

  it("tests a connection and reports the result as text", async () => {
    render(<ConnectionsPage />);
    await screen.findByText("PostgreSQL latihan");
    await userEvent.click(screen.getAllByRole("button", { name: "Uji koneksi" })[0]);
    expect(await screen.findByRole("status")).toHaveTextContent("terhubung (12 ms)");
  });

  it("deactivates only after confirmation", async () => {
    let deleted = 0;
    server.use(
      http.delete("*/api/v1/admin/connections/:id", () => {
        deleted++;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    const confirm = vi.spyOn(window, "confirm").mockReturnValueOnce(false).mockReturnValueOnce(true);
    render(<ConnectionsPage />);
    await screen.findByText("PostgreSQL latihan");
    await userEvent.click(screen.getAllByRole("button", { name: "Nonaktifkan" })[0]);
    expect(deleted).toBe(0);
    await userEvent.click(screen.getAllByRole("button", { name: "Nonaktifkan" })[0]);
    expect(await screen.findByRole("status")).toHaveTextContent("dinonaktifkan");
    expect(deleted).toBe(1);
    expect(confirm).toHaveBeenCalledTimes(2);
  });
});
