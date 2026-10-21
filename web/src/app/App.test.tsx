import { render, screen } from "@testing-library/react";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { MemoryRouter } from "react-router-dom";
import { afterAll, afterEach, beforeAll, describe, expect, it } from "vitest";
import { ApiRequestError } from "../api/client";
import { ErrorState } from "../components/StatusViews";
import { AppRoutes } from "./App";
import { SessionProvider } from "./session";
import { handlers } from "../mocks/handlers";
import { createAuthHandlers } from "../mocks/auth";
import authenticated from "../../../tests/fixtures/api/auth_me_edit.json";

const B = "http://localhost/api/v1";
const server = setupServer(...handlers);
beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => server.resetHandlers(...handlers));
afterAll(() => server.close());

function renderAt(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <SessionProvider>
        <AppRoutes />
      </SessionProvider>
    </MemoryRouter>,
  );
}

const me = (is_platform_admin: boolean, username: string) =>
  http.get(`${B}/auth/me`, () =>
    HttpResponse.json({ data: { id: "u", username, is_platform_admin } }),
  );

describe("app shell", () => {
  it("opens the database page with the shared authenticated mock", async () => {
    renderAt("/databases");
    expect(await screen.findByText(authenticated.data.username)).toBeInTheDocument();
    expect(screen.queryByText(/Halaman login belum tersedia/)).not.toBeInTheDocument();
  });

  it("sends anonymous users to the login route", async () => {
    server.use(...createAuthHandlers(null));
    renderAt("/databases");
    expect(await screen.findByText(/Halaman login belum tersedia/)).toBeInTheDocument();
  });

  it("hides the admin link from non-admins", async () => {
    server.use(me(false, "viewer"));
    renderAt("/databases");
    expect(await screen.findByText("viewer")).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: "Administrasi" })).not.toBeInTheDocument();
  });

  it("shows the admin link to platform admins", async () => {
    server.use(me(true, "root"));
    renderAt("/databases");
    expect(await screen.findByRole("link", { name: "Administrasi" })).toBeInTheDocument();
  });
});

describe("ErrorState", () => {
  it("presents 403 as a denial, not an empty result", () => {
    const err = new ApiRequestError(
      { code: "FORBIDDEN", message: "Akses edit diperlukan.", fields: {}, request_id: "req-9" },
      403,
    );
    render(<ErrorState error={err} />);
    expect(screen.getByRole("alert")).toHaveTextContent("Akses ditolak");
    expect(screen.getByRole("alert")).toHaveTextContent("req-9");
  });
});
