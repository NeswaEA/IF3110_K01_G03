import { queryOptions } from "@tanstack/react-query";
import { api, resetCsrf, unwrap } from "../client";

export const meQuery = queryOptions({
  queryKey: ["auth", "me"],
  queryFn: async ({ signal }) => (await unwrap(api.GET("/auth/me", { signal }))).data,
  staleTime: 60_000,
});

export async function login(username: string, password: string) {
  // A fresh pre-login session; the token of an expired one is rejected.
  resetCsrf();
  const res = await unwrap(api.POST("/auth/login", { body: { username, password } }));
  resetCsrf(); // login rotates the session and with it the CSRF secret
  return res.data.user;
}

export async function logout() {
  await unwrap(api.POST("/auth/logout", { body: {} }));
  resetCsrf();
}
