import { test, expect } from "@playwright/test";
import { engines, login, metadata, pathFor } from "../helpers.mjs";

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    try { return (await request.get("/healthz")).status(); } catch { return 0; }
  }, { timeout: 30_000 }).toBe(200);
});

test("admin tests both source connections before migrations become ready", async ({ page }) => {
  await login(page, "fixture-admin");
  await page.goto("/admin/connections");
  for (const engine of engines) {
    const row = page.getByRole("row").filter({ has: page.getByRole("cell", { name: engine.name, exact: true }) });
    await expect(row).toContainText("Belum pernah");
    await expect(row).toContainText("Belum siap migrasi");
    await row.getByRole("button", { name: "Uji", exact: true }).click();
    await expect(page.getByText(`${engine.name}: terhubung`)).toBeVisible();
    await expect(row).not.toContainText("Belum siap migrasi");
    await expect(row).toContainText("Siap migrasi");
  }
  const comparisons = await metadata("SELECT migration_ready FROM database_connections WHERE id IN ($1,$2)", engines.map((e) => e.desired));
  expect(comparisons.map((row) => row.migration_ready)).toEqual([false, false]);
});

test("anonymous pages redirect while APIs, missing pages and missing assets retain their status", async ({ page, request }) => {
  await page.goto(pathFor(engines[0]));
  await expect(page).toHaveURL(/\/login\?next=/);
  expect((await request.get("/api/v1/databases")).status()).toBe(401);
  expect((await request.get("/api/v1/not-an-endpoint")).status()).toBe(404);
  await login(page);
  // The SPA serves index.html for unknown pages and renders its own not-found view.
  await page.goto("/not-a-page");
  await expect(page.getByText("Halaman tidak ditemukan")).toBeVisible();
  expect((await page.goto("/assets/not-an-asset.js")).status()).toBe(404);
});

test("view-only users cannot run the schema diff", async ({ page }) => {
  await login(page, "viewer");
  // Sent from the page so the browser supplies Origin and the session cookie like the SPA does.
  const result = await page.evaluate(async (engine) => {
    const csrf = await fetch("/api/v1/auth/csrf");
    const { data: { csrf_token } } = await csrf.json();
    const response = await fetch(`/api/v1/databases/${engine.id}/schema-diff`, {
      method: "POST",
      headers: { "Content-Type": "application/json", "X-CSRF-Token": csrf_token },
      body: JSON.stringify({ desired_database_id: engine.desired, schema: engine.schema }),
    });
    return { csrfStatus: csrf.status, status: response.status, body: await response.json() };
  }, engines[0]);
  expect(result.csrfStatus).toBe(200);
  expect(result.status).toBe(403);
  expect(result.body.error.code).toBe("FORBIDDEN");
});
