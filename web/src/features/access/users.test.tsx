import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import leaderRequired from "../../../../tests/fixtures/api/access_error_leader_required.json";
import list from "../../../../tests/fixtures/api/users_list.json";
import validation from "../../../../tests/fixtures/api/users_error_validation.json";
import { createAuthHandlers } from "@/mocks/auth";
import { renderApp, server } from "@/test-utils";

const API = "*/api/v1";
const admin = { id: "10000000-0000-4000-8000-000000000001", username: "fixture-admin", is_platform_admin: true };
const lead = list.data.find((u) => u.username === "lead")!;

describe("user administration", () => {
  it("lists accounts with their state and marks the signed-in admin", async () => {
    server.use(...createAuthHandlers(admin));
    renderApp("/admin/users");
    const table = await screen.findByRole("table");
    expect(within(table).getAllByRole("row")).toHaveLength(list.data.length + 1);
    expect(within(table).getByText("(Anda)")).toBeInTheDocument();
    expect(within(table).getByText("Nonaktif")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Pengguna" })).toBeInTheDocument();
  });

  it("sends a non-admin back to the database list", async () => {
    server.use(...createAuthHandlers({ ...admin, is_platform_admin: false }));
    const { router } = renderApp("/admin/users");
    await waitFor(() => expect(router.state.location.pathname).toBe("/databases"));
  });

  it("shows the password rule from the API next to the field", async () => {
    server.use(...createAuthHandlers(admin), http.post(`${API}/admin/users`, () => HttpResponse.json(validation, { status: 400 })));
    renderApp("/admin/users");
    await userEvent.click(await screen.findByRole("button", { name: "Tambah pengguna" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText("Username"), "baru");
    await userEvent.type(within(dialog).getByLabelText("Password awal"), "password-cukup-panjang");
    await userEvent.click(within(dialog).getByRole("button", { name: "Buat pengguna" }));
    expect(await within(dialog).findByText(validation.error.fields.password)).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Password awal")).toHaveAttribute("aria-invalid", "true");
  });

  it("asks before deactivating and explains a leader refusal", async () => {
    let body: unknown;
    server.use(
      ...createAuthHandlers(admin),
      http.patch(`${API}/admin/users/:userId`, async ({ request }) => {
        body = await request.json();
        return HttpResponse.json(leaderRequired, { status: 409 });
      }),
    );
    renderApp("/admin/users");
    const row = (await screen.findByText(lead.username)).closest("tr")!;
    await userEvent.click(within(row).getByRole("button", { name: "Nonaktifkan" }));
    const dialog = await screen.findByRole("dialog", { name: `Nonaktifkan ${lead.username}?` });
    expect(body).toBeUndefined();
    await userEvent.click(within(dialog).getByRole("button", { name: "Nonaktifkan" }));
    expect(await screen.findByText(leaderRequired.error.message)).toBeInTheDocument();
    expect(body).toEqual({ active: false });
  });

  it("replaces a password without showing it back", async () => {
    let sent: unknown;
    server.use(
      ...createAuthHandlers(admin),
      http.put(`${API}/admin/users/:userId/password`, async ({ request }) => {
        sent = await request.json();
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp("/admin/users");
    const row = (await screen.findByText(lead.username)).closest("tr")!;
    await userEvent.click(within(row).getByRole("button", { name: "Ganti password" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.type(within(dialog).getByLabelText(`Password baru untuk ${lead.username}`), "password-baru-rahasia");
    await userEvent.click(within(dialog).getByRole("button", { name: "Ganti password" }));
    await waitFor(() => expect(sent).toEqual({ password: "password-baru-rahasia" }));
    expect(await screen.findByText(/Password lead diganti/)).toBeInTheDocument();
    expect(document.body.textContent).not.toContain("password-baru-rahasia");
  });
});
