import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import notLeader from "../../../../tests/fixtures/api/reviews_error_not_leader.json";
import pending from "../../../../tests/fixtures/api/reviews_change_request_pending.json";
import type { ChangeRequest } from "@/api/queries/reviews";
import { createAuthHandlers } from "@/mocks/auth";
import { server } from "@/test-utils";
import { ApprovalPanel } from "./ApprovalPanel";

const API = "*/api/v1";
const request = pending.data as ChangeRequest;
const lead = { id: "10000000-0000-4000-8000-000000000005", username: "lead", is_platform_admin: false };
const editor = { id: "10000000-0000-4000-8000-000000000003", username: "editor", is_platform_admin: true };

function renderPanel(value: ChangeRequest = request) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <ApprovalPanel request={value} />
    </QueryClientProvider>,
  );
}

describe("ApprovalPanel", () => {
  it("lists every required group with its leader and decision as text", async () => {
    server.use(...createAuthHandlers(lead));
    renderPanel();
    const items = screen.getAllByRole("listitem");
    expect(items).toHaveLength(2);
    expect(within(items[0]).getByText("Disetujui")).toBeInTheDocument();
    expect(within(items[1]).getByText("Leader: lead")).toBeInTheDocument();
    expect(within(items[1]).getByText("Menunggu")).toBeInTheDocument();
    expect(await screen.findByRole("button", { name: "Setujui" })).toBeInTheDocument();
    expect(screen.getByText("fixture-edit", { selector: "span" })).toBeInTheDocument();
  });

  it("offers no decision to someone who leads no pending group, even an admin", async () => {
    server.use(...createAuthHandlers(editor));
    renderPanel();
    await waitFor(() => expect(screen.getByText("Menunggu review")).toBeInTheDocument());
    expect(screen.queryByRole("button", { name: "Setujui" })).not.toBeInTheDocument();
  });

  it("offers no decision once the request is closed", async () => {
    server.use(...createAuthHandlers(lead));
    renderPanel({ ...request, status: "rejected" });
    await screen.findByText("Ditolak", { selector: "[data-status]" });
    expect(screen.queryByRole("button", { name: "Tolak" })).not.toBeInTheDocument();
  });

  it("approves with an optional comment", async () => {
    let sent: unknown;
    server.use(
      ...createAuthHandlers(lead),
      http.post(`${API}/databases/:id/change-requests/:rid/approve`, async ({ request: r }) => {
        sent = await r.json();
        return HttpResponse.json({ data: { ...request, status: "approved" } });
      }),
    );
    renderPanel();
    await userEvent.click(await screen.findByRole("button", { name: "Setujui" }));
    const dialog = await screen.findByRole("dialog");
    await userEvent.click(within(dialog).getByRole("button", { name: "Setujui" }));
    await waitFor(() => expect(sent).toEqual({}));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
  });

  it("requires a reason to reject and shows a refusal from the API", async () => {
    let sent: unknown;
    server.use(
      ...createAuthHandlers(lead),
      http.post(`${API}/databases/:id/change-requests/:rid/reject`, async ({ request: r }) => {
        sent = await r.json();
        return HttpResponse.json(notLeader, { status: 403 });
      }),
    );
    renderPanel();
    await userEvent.click(await screen.findByRole("button", { name: "Tolak" }));
    const dialog = await screen.findByRole("dialog");
    const reason = within(dialog).getByLabelText("Alasan penolakan");
    expect(reason).toBeRequired();
    await userEvent.type(reason, "Tambahkan indeks dulu.");
    await userEvent.click(within(dialog).getByRole("button", { name: "Tolak" }));
    expect(await within(dialog).findByText(notLeader.error.message)).toBeInTheDocument();
    expect(sent).toEqual({ comment: "Tambahkan indeks dulu." });
  });
});
