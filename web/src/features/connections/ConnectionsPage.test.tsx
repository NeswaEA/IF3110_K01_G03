import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { setCsrfToken } from "../../api/client";
import connectionsList from "../../../../tests/fixtures/api/connections_list.json";
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
  window.history.replaceState(null, "", "/");
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

  it("pages past the first 200 registrations through the page URL", async () => {
    const offsets: (string | null)[] = [];
    server.use(
      http.get("*/api/v1/admin/connections", ({ request }) => {
        const offset = new URL(request.url).searchParams.get("offset");
        offsets.push(offset);
        const data = offset === "0" ? connectionsList.data : offset === "200" ? [connectionsList.data[1]] : [];
        return HttpResponse.json({ data, page: { limit: 200, offset: Number(offset), has_more: offset === "0" } });
      }),
    );
    const { unmount } = render(<ConnectionsPage />);
    await screen.findByText("PostgreSQL latihan");
    expect(screen.getByRole("link", { name: "Berikutnya" })).toHaveAttribute("href", "/admin/connections?offset=200");
    expect(screen.queryByRole("link", { name: "Sebelumnya" })).not.toBeInTheDocument();
    unmount();

    window.history.replaceState(null, "", "/admin/connections?offset=200");
    const second = render(<ConnectionsPage />);
    await screen.findByText("SQLite latihan");
    expect(screen.getByText("Menampilkan koneksi 201 sampai 201.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Sebelumnya" })).toHaveAttribute("href", "/admin/connections");
    expect(screen.queryByRole("link", { name: "Berikutnya" })).not.toBeInTheDocument();
    // actions reload the same page, not the first one
    await userEvent.click(screen.getByRole("button", { name: "Uji koneksi" }));
    await screen.findByRole("status");
    await vi.waitFor(() => expect(offsets).toEqual(["0", "200", "200"]));
    second.unmount();

    window.history.replaceState(null, "", "/admin/connections?offset=400");
    render(<ConnectionsPage />);
    expect(await screen.findByText("Tidak ada koneksi pada halaman ini.")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Halaman pertama" })).toHaveAttribute("href", "/admin/connections");
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

  it("edits with the opened revision and sends only changed fields, keeping the password", async () => {
    server.use(
      http.patch("*/api/v1/admin/connections/:id", async ({ request }) => {
        posted = await request.json();
        return HttpResponse.json({ data: {} });
      }),
    );
    render(<ConnectionsPage />);
    await screen.findByText("PostgreSQL latihan");
    await userEvent.click(screen.getAllByRole("button", { name: "Ubah" })[0]);
    const form = screen.getByRole("form", { name: "Ubah koneksi PostgreSQL latihan" });
    const edit = within(form);
    expect(edit.getByLabelText("Host")).toHaveValue("target-postgres");
    expect(edit.getByLabelText("Password")).toHaveValue("");
    expect(edit.getByLabelText("Password")).not.toBeRequired();
    await userEvent.clear(edit.getByLabelText("Port"));
    await userEvent.type(edit.getByLabelText("Port"), "5433");
    await userEvent.click(edit.getByRole("button", { name: "Simpan perubahan" }));
    await vi.waitFor(() => expect(posted).toEqual({ revision: 1, config: { port: 5433 } }));
    expect(await screen.findByRole("status")).toHaveTextContent("PostgreSQL latihan diperbarui.");
    expect(screen.queryByRole("form", { name: /Ubah koneksi/ })).not.toBeInTheDocument();
  });

  it("does not send a request when nothing changed", async () => {
    let patched = 0;
    server.use(
      http.patch("*/api/v1/admin/connections/:id", () => {
        patched++;
        return HttpResponse.json({ data: {} });
      }),
    );
    render(<ConnectionsPage />);
    await screen.findByText("SQLite latihan");
    await userEvent.click(screen.getAllByRole("button", { name: "Ubah" })[1]);
    await userEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    expect(await screen.findByRole("status")).toHaveTextContent("tidak ada perubahan");
    expect(patched).toBe(0);
  });

  it("shows a revision conflict instead of overwriting", async () => {
    server.use(
      http.get("*/api/v1/admin/connections", () =>
        HttpResponse.json({ data: [{ ...connectionsList.data[1], revision: 3 }], page: connectionsList.page }),
      ),
    );
    render(<ConnectionsPage />);
    await screen.findByText("SQLite latihan");
    await userEvent.click(screen.getByRole("button", { name: "Ubah" }));
    await userEvent.clear(screen.getByLabelText(/Berkas SQLite/));
    await userEvent.type(screen.getByLabelText(/Berkas SQLite/), "lain.sqlite");
    await userEvent.click(screen.getByRole("button", { name: "Simpan perubahan" }));
    expect(await screen.findByText(/Koneksi berubah sejak dibaca/)).toBeInTheDocument();
    expect(screen.getByRole("form", { name: /Ubah koneksi/ })).toBeInTheDocument();
  });
});
