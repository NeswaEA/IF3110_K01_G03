// Shared helpers for the k6 scripts: per-VU login with backoff, CSRF and
// execution polling. Only the HTTP API is used, like the SPA.
import { fail, sleep } from "k6";
import http from "k6/http";

export const BASE = __ENV.BASE_URL || "http://localhost:8080";
export const API = `${BASE}/api/v1`;
// The API checks Origin against APP_ORIGIN on every mutation.
const ORIGIN = __ENV.APP_ORIGIN || "http://localhost:8080";

export const TERMINAL = ["succeeded", "failed", "partial", "partially_reverted", "cancelled", "outcome_unknown"];

export function jsonHeaders(csrf) {
  return { "Content-Type": "application/json", "X-CSRF-Token": csrf, Origin: ORIGIN };
}

function csrfToken() {
  const res = http.get(`${API}/auth/csrf`, { tags: { name: "csrf" } });
  if (res.status !== 200) fail(`csrf: HTTP ${res.status}`);
  return res.json("data.csrf_token");
}

/**
 * Signs in on the VU's own cookie jar. Login is rate limited, so a 429 waits
 * with full-jitter backoff instead of failing the run.
 */
export function login(username, password) {
  for (let attempt = 0; attempt < 8; attempt++) {
    const res = http.post(`${API}/auth/login`, JSON.stringify({ username, password }), {
      headers: jsonHeaders(csrfToken()),
      tags: { name: "login" },
      responseCallback: http.expectedStatuses(200, 429),
    });
    if (res.status === 200) return { csrf: csrfToken() }; // login rotates the session and its token
    sleep(Math.random() * Math.min(30, 2 ** attempt));
  }
  fail(`login ${username}: still rate limited`);
}

export function post(session, path, body, name) {
  return http.post(`${API}${path}`, JSON.stringify(body ?? {}), { headers: jsonHeaders(session.csrf), tags: { name } });
}

export function get(path, name) {
  return http.get(`${API}${path}`, { tags: { name } });
}

/** Polls an execution until it reaches a final status or timeoutSec passes. */
export function waitForExecution(databaseId, executionId, timeoutSec) {
  const deadline = Date.now() + timeoutSec * 1000;
  while (Date.now() < deadline) {
    const res = get(`/databases/${databaseId}/executions/${executionId}`, "execution");
    if (res.status === 200) {
      const e = res.json("data");
      if (TERMINAL.includes(e.status)) return e;
    }
    sleep(0.5);
  }
  return null;
}
