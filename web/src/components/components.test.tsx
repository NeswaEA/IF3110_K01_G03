import { render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { http, HttpResponse } from "msw";
import { describe, expect, it } from "vitest";
import explainPg from "../../../tests/fixtures/api/explain_pg.json";
import type { Schemas } from "@/api/client";
import { api, unwrap } from "@/api/client";
import { ExplainPanel } from "@/features/explain/ExplainPanel";
import { renderApp, server } from "@/test-utils";
import { StatusBadge, statusLabel } from "./StatusBadge";

const API = "*/api/v1";
const PG = "30000000-0000-4000-8000-000000000001";

describe("StatusBadge", () => {
  const all = {
    execution: ["queued", "held", "running", "succeeded", "failed", "partial", "partially_reverted", "outcome_unknown", "cancelled"],
    request: ["pending_review", "approved", "rejected", "cancelled", "executed"],
    migration: ["pending", "applied", "rolled_back", "superseded", "partial"],
  } satisfies { execution: Schemas["ExecutionStatus"][]; request: Schemas["RequestStatus"][]; migration: Schemas["MigrationStatus"][] };

  it("gives every status an Indonesian label and an icon", () => {
    for (const [kind, statuses] of Object.entries(all)) {
      for (const status of statuses) {
        const label = statusLabel({ kind, status } as Parameters<typeof statusLabel>[0]);
        expect(label, `${kind}.${status}`).not.toBe(status);
      }
    }
    const { container } = render(<StatusBadge kind="execution" status="running" />);
    expect(screen.getByText("Berjalan")).toBeInTheDocument();
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden");
  });
});

describe("ExplainPanel", () => {
  it("shows the plan tree and resets when the SQL changes", async () => {
    let body: unknown;
    server.use(
      http.post(`${API}/databases/:id/explain`, async ({ request }) => {
        body = await request.json();
        return HttpResponse.json(explainPg);
      }),
    );
    const { rerender } = render(<ExplainPanel databaseId={PG} sql="SELECT * FROM items" canExplain />);
    await userEvent.click(screen.getByRole("button", { name: "Analisis rencana (EXPLAIN)" }));
    expect(await screen.findByText(/mode: estimasi/)).toBeInTheDocument();
    expect(body).toEqual({ sql: "SELECT * FROM items", mode: "estimate" });
    rerender(<ExplainPanel databaseId={PG} sql="SELECT 1" canExplain />);
    expect(screen.queryByText(/mode: estimasi/)).not.toBeInTheDocument();
  });

  it("is disabled without edit access and shows engine errors", async () => {
    const { rerender } = render(<ExplainPanel databaseId={PG} sql="SELECT 1" canExplain={false} />);
    expect(screen.getByRole("button", { name: /EXPLAIN/ })).toBeDisabled();
    rerender(<ExplainPanel databaseId={PG} sql="SELECT 1; SELECT 2" canExplain />);
    await userEvent.click(screen.getByRole("button", { name: /EXPLAIN/ }));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });
});

describe("session expiry", () => {
  it("keeps unsaved work: a blocking route gets the re-login dialog instead of a redirect", async () => {
    const { router } = renderApp(`/databases/${PG}`);
    await screen.findByRole("heading", { name: /Schema public/ });
    // what useBlocker registers on a route with unsaved changes
    const unblock = router.history.block({ blockerFn: () => true });
    let mutations = 0;
    server.use(
      http.get(`${API}/databases`, () =>
        HttpResponse.json({ error: { code: "UNAUTHENTICATED", message: "Sesi berakhir.", fields: {}, request_id: "r" } }, { status: 401 }),
      ),
      http.post(`${API}/admin/connections/:id/test`, () => {
        mutations++;
        return HttpResponse.json({});
      }),
    );
    await unwrap(api.GET("/databases")).catch(() => {});
    expect(await screen.findByRole("dialog", { name: "Sesi berakhir" })).toBeInTheDocument();
    expect(router.state.location.pathname).toBe(`/databases/${PG}`);

    server.use(
      http.post(`${API}/auth/login`, () =>
        HttpResponse.json({ data: { user: { id: "10000000-0000-4000-8000-000000000003", username: "editor", is_platform_admin: false } } }),
      ),
    );
    await userEvent.type(screen.getByLabelText("Username"), "editor");
    await userEvent.type(screen.getByLabelText("Password"), "secret-password");
    await userEvent.click(screen.getByRole("button", { name: "Login" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).not.toBeInTheDocument());
    expect(mutations).toBe(0); // nothing is resent
    unblock();
  });

  it("without unsaved work, goes to login with next", async () => {
    const { router } = renderApp(`/databases/${PG}`);
    await screen.findByRole("heading", { name: /Schema public/ });
    server.use(
      http.get(`${API}/databases`, () =>
        HttpResponse.json({ error: { code: "UNAUTHENTICATED", message: "Sesi berakhir.", fields: {}, request_id: "r" } }, { status: 401 }),
      ),
    );
    await unwrap(api.GET("/databases")).catch(() => {});
    await waitFor(() => expect(router.state.location.pathname).toBe("/login"));
    expect(router.state.location.search).toEqual({ next: `/databases/${PG}` });
  });
});
