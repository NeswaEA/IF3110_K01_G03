import { http, HttpResponse, type RequestHandler } from "msw";
import csrf from "../../../tests/fixtures/api/auth_csrf.json";
import authenticated from "../../../tests/fixtures/api/auth_me_edit.json";
import unauthenticated from "../../../tests/fixtures/api/auth_error_unauthenticated.json";
import type { CurrentUser } from "../api/types";

export function createAuthHandlers(user: CurrentUser | null = authenticated.data): RequestHandler[] {
  return [
    http.get("*/api/v1/auth/csrf", () =>
      HttpResponse.json(csrf, { headers: { "X-Request-ID": "req-auth-csrf" } }),
    ),
    http.get("*/api/v1/auth/me", () =>
      user === null
        ? HttpResponse.json(unauthenticated, {
            status: 401,
            headers: { "X-Request-ID": unauthenticated.error.request_id },
          })
        : HttpResponse.json({ data: user }, { headers: { "X-Request-ID": "req-auth-me" } }),
    ),
  ];
}
