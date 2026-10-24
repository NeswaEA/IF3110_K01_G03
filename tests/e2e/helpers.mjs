import { randomUUID } from "node:crypto";
import { DatabaseSync } from "node:sqlite";
import pg from "pg";
import { expect, request } from "@playwright/test";

export const origin = "http://localhost:8080";
export const password = "e2e fixture password 123";
export const engines = [
  { driver: "postgres", id: "30000000-0000-4000-8000-000000000001", desired: "30000000-0000-4000-8000-000000000003", name: "PostgreSQL demo", schema: "public" },
  { driver: "sqlite", id: "30000000-0000-4000-8000-000000000002", desired: "30000000-0000-4000-8000-000000000004", name: "SQLite demo", schema: "main" },
];
export const pathFor = (engine) => `/databases/${engine.id}`;
export const uniqueName = () => "e2e_" + randomUUID().replaceAll("-", "");

export async function login(page, username = "editor") {
  await page.goto("/login");
  await page.getByLabel("Username", { exact: true }).fill(username);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Masuk", exact: true }).click();
  await expect(page).toHaveURL(origin + "/databases");
}

export async function mutate(api, method, path, data, status = 200, headers = {}) {
  const token = await api.get(origin + "/api/v1/auth/csrf");
  expect(token.status()).toBe(200);
  const csrf = (await token.json()).data.csrf_token;
  const response = await api.fetch(origin + "/api/v1" + path, {
    method, data, headers: { Origin: origin, "X-CSRF-Token": csrf, ...headers },
  });
  expect(response.status(), await response.text()).toBe(status);
  return status === 204 ? null : response.json();
}

export async function apiUser(username) {
  const api = await request.newContext({ baseURL: origin });
  try {
    await mutate(api, "POST", "/auth/login", { username, password });
    return api;
  } catch (err) { await api.dispose(); throw err; }
}

export async function ready() {
  const api = await apiUser("fixture-admin");
  try {
    for (const engine of engines) await mutate(api, "POST", `/admin/connections/${engine.id}/test`, {});
  } finally { await api.dispose(); }
}

export async function getData(api, path) {
  const response = await api.get(origin + "/api/v1" + path);
  expect(response.status(), await response.text()).toBe(200);
  return (await response.json()).data;
}

export async function createDraft(api, engine, sql, title = uniqueName()) {
  const head = await getData(api, `${pathFor(engine)}/migration-head`);
  const response = await mutate(api, "POST", `${pathFor(engine)}/migration-drafts`, { title, sql, expected_version: head.version }, 201);
  return response.data;
}

export async function saveDraft(page, engine, sql, title = uniqueName()) {
  await page.goto(`${pathFor(engine)}/migrations`);
  await page.getByLabel("Judul", { exact: true }).fill(title);
  await page.getByRole("textbox", { name: "SQL migrasi" }).fill(sql);
  const response = page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/migration-drafts"));
  await page.getByRole("button", { name: "Simpan draft", exact: true }).click();
  const saved = await response;
  expect(saved.status()).toBe(201);
  await expect(page.getByRole("status").filter({ hasText: /^Draft tersimpan\.$/ })).toBeVisible();
  return (await saved.json()).data;
}

export async function execute(page, engine, { double = false } = {}) {
  const response = page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/execute"));
  await page.getByRole("button", { name: "Jalankan migrasi", exact: true }).click({ clickCount: double ? 2 : 1 });
  const received = await response;
  const body = await received.json();
  const run = body.data ?? await getData(page.request, `${pathFor(engine)}/migration-runs/${body.error.run_id}`);
  await expect(page.getByRole("region", { name: "Detail eksekusi" })).toBeVisible();
  return { run, status: received.status() };
}

async function postgres(database, fn) {
  const client = new pg.Client(database === "metadata"
    ? "postgres://metadata_owner:e2e_metadata@metadata:5432/metadata?sslmode=disable"
    : `postgres://target_admin:e2e_target@target:5432/${database}?sslmode=disable`);
  await client.connect();
  try { return await fn(client); } finally { await client.end(); }
}

export const metadata = (sql, params = []) => postgres("metadata", async (client) => (await client.query(sql, params)).rows);

export async function targetRows(engine, sql, desired = false) {
  if (engine.driver === "postgres") return postgres(desired ? "demo_desired" : "demo", async (client) => (await client.query(sql)).rows);
  const db = new DatabaseSync(`/data/sqlite/${desired ? "demo_desired" : "demo"}.sqlite`, { timeout: 5000 });
  try { return db.prepare(sql).all(); } finally { db.close(); }
}

export async function targetExec(engine, sql) {
  if (engine.driver === "postgres") return postgres("demo", (client) => client.query(`SET ROLE platform_editor; ${sql}`));
  const db = new DatabaseSync("/data/sqlite/demo.sqlite", { timeout: 5000 });
  try { db.exec(sql); } finally { db.close(); }
}

export async function ledgerCount(engine, runID) {
  expect(runID).toMatch(/^[0-9a-f-]{36}$/);
  const table = engine.driver === "postgres" ? "_platform.migration_ledger" : "_platform_migration_ledger";
  return Number((await targetRows(engine, `SELECT count(*) AS count FROM ${table} WHERE run_id='${runID}'`))[0].count);
}

export async function blockFinalization(draftID) {
  await metadata(`CREATE FUNCTION e2e_reject_finalization() RETURNS trigger LANGUAGE plpgsql AS $$
    BEGIN
      IF NEW.draft_id::text = TG_ARGV[0] AND NEW.status = 'succeeded' THEN
        RAISE EXCEPTION 'e2e metadata finalization unavailable';
      END IF;
      RETURN NEW;
    END; $$`);
  expect(draftID).toMatch(/^[0-9a-f-]{36}$/);
  await metadata(`CREATE TRIGGER e2e_finalize_failure BEFORE UPDATE ON migration_runs
    FOR EACH ROW EXECUTE FUNCTION e2e_reject_finalization('${draftID}')`);
}

export async function unblockFinalization() {
  await metadata("DROP TRIGGER IF EXISTS e2e_finalize_failure ON migration_runs; DROP FUNCTION IF EXISTS e2e_reject_finalization()");
}
