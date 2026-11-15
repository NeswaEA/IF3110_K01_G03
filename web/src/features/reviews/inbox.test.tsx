import { screen, waitFor, within } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import inbox from "../../../../tests/fixtures/api/reviews_inbox.json";
import { createAuthHandlers } from "@/mocks/auth";
import { FakeEventSource, renderApp, server } from "@/test-utils";

const API = "*/api/v1";
const lead = { id: "10000000-0000-4000-8000-000000000005", username: "lead", is_platform_admin: false };
const [item] = inbox.data;

describe("review inbox", () => {
  it("lists each waiting request with its database and the approval panel", async () => {
    server.use(...createAuthHandlers(lead));
    renderApp("/reviews");
    const entry = (await screen.findByRole("heading", { name: item.request.migration_title })).closest("li")!;
    expect(within(entry).getByText(new RegExp(item.database.name))).toBeInTheDocument();
    expect(within(entry).getByRole("button", { name: "Setujui" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Menunggu persetujuan" })).toBeInTheDocument();
  });

  it("says when nothing waits", async () => {
    server.use(...createAuthHandlers(lead), http.get(`${API}/reviews`, () => HttpResponse.json({ data: [], page: { limit: 20, offset: 0, has_more: false } })));
    renderApp("/reviews");
    expect(await screen.findByText("Tidak ada yang menunggu keputusan Anda")).toBeInTheDocument();
  });

  it("refreshes when a request changes in one of its databases", async () => {
    let calls = 0;
    server.use(
      ...createAuthHandlers(lead),
      http.get(`${API}/reviews`, () => {
        calls++;
        return HttpResponse.json(inbox);
      }),
    );
    renderApp("/reviews");
    await screen.findByRole("heading", { name: item.request.migration_title });
    const source = FakeEventSource.instances.find((s) => s.url.endsWith(`/databases/${item.database.id}/events`));
    expect(source).toBeDefined();
    const before = calls;
    source!.emit("request.updated");
    await waitFor(() => expect(calls).toBeGreaterThan(before));
  });
});
