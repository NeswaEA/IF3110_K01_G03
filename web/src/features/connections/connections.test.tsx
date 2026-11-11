import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import list from "../../../../tests/fixtures/api/connections_list.json";
import validation from "../../../../tests/fixtures/api/connections_error_validation.json";
import { createAuthHandlers } from "@/mocks/auth";
import { renderApp, server } from "@/test-utils";

const API = "*/api/v1";
const admin = { id: "10000000-0000-4000-8000-000000000001", username: "admin", is_platform_admin: true };

describe("connections admin", () => {
  it("lists registrations without secrets and tests one", async () => {
    server.use(...createAuthHandlers(admin));
    renderApp("/admin/connections");
    const table = await screen.findByRole("table");
    expect(within(table).getByText(list.data[0].name)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/postgres(ql)?:\/\/|sslmode=/i); // no DSN is ever rendered
    await userEvent.click(within(table).getAllByRole("button", { name: "Uji" })[0]);
    expect(await screen.findByText(/terhubung dalam 12 ms/)).toBeInTheDocument();
  });

  it("shows field errors from the API next to the field", async () => {
    server.use(...createAuthHandlers(admin), http.post(`${API}/admin/connections`, () => HttpResponse.json(validation, { status: 400 })));
    renderApp("/admin/connections");
    await userEvent.click(await screen.findByRole("button", { name: "Tambah koneksi" }));
    const dialog = await screen.findByRole("dialog");
    for (const [label, value] of [["Nama", "Baru"], ["Host", "db"], ["Port", "5432"], ["Nama database", "d"], ["Username", "u"], ["Password", "p"], ["Role editor terbatas", "e"]]) {
      await userEvent.type(within(dialog).getByLabelText(label), value);
    }
    await userEvent.click(within(dialog).getByRole("button", { name: "Simpan" }));
    const [field, message] = Object.entries(validation.error.fields)[0];
    await waitFor(() => expect(within(dialog).getByText(message)).toBeInTheDocument());
    expect(field).toMatch(/^(name|config\.)/);
  });

  it("asks before deactivating", async () => {
    let deleted = false;
    server.use(
      ...createAuthHandlers(admin),
      http.delete(`${API}/admin/connections/:id`, () => {
        deleted = true;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp("/admin/connections");
    await userEvent.click((await screen.findAllByRole("button", { name: "Nonaktifkan" }))[0]);
    expect(await screen.findByRole("dialog", { name: "Nonaktifkan koneksi?" })).toBeInTheDocument();
    expect(deleted).toBe(false);
    await userEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "Nonaktifkan" }));
    await waitFor(() => expect(deleted).toBe(true));
  });
});
