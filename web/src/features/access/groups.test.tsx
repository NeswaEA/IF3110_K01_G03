import { screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import connections from "../../../../tests/fixtures/api/connections_list.json";
import leaderRequired from "../../../../tests/fixtures/api/groups_error_leader_required.json";
import groups from "../../../../tests/fixtures/api/groups_list.json";
import { createAuthHandlers } from "@/mocks/auth";
import { renderApp, server } from "@/test-utils";

const API = "*/api/v1";
const admin = { id: "10000000-0000-4000-8000-000000000001", username: "fixture-admin", is_platform_admin: true };
const [led, , leaderless] = groups.data;

describe("group administration", () => {
  it("lists groups with their leader and opens one", async () => {
    server.use(...createAuthHandlers(admin));
    const { router } = renderApp("/admin/groups");
    const table = await screen.findByRole("table");
    expect(within(table).getByText("Belum ditetapkan")).toBeInTheDocument();
    await userEvent.click(within(table).getByRole("link", { name: led.name }));
    await waitFor(() => expect(router.state.location.pathname).toBe(`/admin/groups/${led.id}`));
    expect(await screen.findByRole("heading", { name: led.name })).toBeInTheDocument();
    expect(screen.getByText(`Leader: ${led.leader!.username}`)).toBeInTheDocument();
  });

  it("keeps the open tab in the URL", async () => {
    server.use(...createAuthHandlers(admin));
    const { router } = renderApp(`/admin/groups/${led.id}`);
    await userEvent.click(await screen.findByRole("tab", { name: "Hak akses" }));
    await waitFor(() => expect(router.state.location.search).toEqual({ tab: "grants" }));
    expect(await screen.findByText(connections.data[0].name)).toBeInTheDocument();
  });

  it("sets the leader from the active members", async () => {
    let sent: unknown;
    server.use(
      ...createAuthHandlers(admin),
      http.put(`${API}/admin/groups/:groupId/leader`, async ({ request }) => {
        sent = await request.json();
        return HttpResponse.json({ data: { ...leaderless, leader: { id: "10000000-0000-4000-8000-000000000003", username: "editor" } } });
      }),
    );
    renderApp(`/admin/groups/${leaderless.id}?tab=leader`);
    await userEvent.click(await screen.findByRole("combobox", { name: "Leader" }));
    await userEvent.click(await screen.findByRole("option", { name: "editor" }));
    await userEvent.click(screen.getByRole("button", { name: "Simpan leader" }));
    await waitFor(() => expect(sent).toEqual({ user_id: "10000000-0000-4000-8000-000000000003" }));
    expect(await screen.findByText(/editor sekarang leader/)).toBeInTheDocument();
  });

  it("warns that a group without a leader cannot get access and shows the refusal", async () => {
    server.use(...createAuthHandlers(admin), http.put(`${API}/admin/groups/:groupId/grants/:databaseId`, () => HttpResponse.json(leaderRequired, { status: 409 })));
    renderApp(`/admin/groups/${leaderless.id}?tab=grants`);
    expect(await screen.findByText("Tetapkan leader sebelum memberi hak akses")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("combobox", { name: "Database" }));
    await userEvent.click(await screen.findByRole("option", { name: connections.data[1].name }));
    await userEvent.click(screen.getByRole("button", { name: "Beri akses" }));
    expect(await screen.findByText(leaderRequired.error.message)).toBeInTheDocument();
  });

  it("asks before removing the leader and says how to replace them", async () => {
    let removed = false;
    server.use(
      ...createAuthHandlers(admin),
      http.delete(`${API}/admin/groups/:groupId/members/:userId`, () => {
        removed = true;
        return new HttpResponse(null, { status: 204 });
      }),
    );
    renderApp(`/admin/groups/${groups.data[1].id}`);
    const row = (await screen.findByText("lead")).closest("tr")!;
    expect(within(row).getByText("Leader")).toBeInTheDocument();
    await userEvent.click(within(row).getByRole("button", { name: "Keluarkan" }));
    expect(await screen.findByText(/Tetapkan leader lain di tab Leader/)).toBeInTheDocument();
    expect(removed).toBe(false);
  });

  it("shows a 404 for a group that does not exist", async () => {
    server.use(...createAuthHandlers(admin));
    renderApp("/admin/groups/20000000-0000-4000-8000-000000000098");
    expect(await screen.findByText(/tidak ditemukan/i)).toBeInTheDocument();
  });
});
