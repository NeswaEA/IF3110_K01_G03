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
    await expect(row).toContainText("belum diuji");
    await row.getByRole("button", { name: "Uji koneksi", exact: true }).click();
    await expect(page.getByRole("status").filter({ hasText: `${engine.name}: terhubung` })).toBeVisible();
    await expect(row).toContainText("siap migrasi");
  }
  const comparisons = await metadata("SELECT migration_ready FROM database_connections WHERE id IN ($1,$2)", engines.map((e) => e.desired));
  expect(comparisons.map((row) => row.migration_ready)).toEqual([false, false]);
});

test("anonymous pages redirect while APIs, missing pages and missing assets retain their status", async ({ page, request }) => {
  await page.goto(pathFor(engines[0]));
  await expect(page).toHaveURL(/\/login\?next=/);
  expect((await request.get("/api/v1/databases")).status()).toBe(401);
  await login(page);
  expect((await page.goto("/not-a-page")).status()).toBe(404);
  expect((await page.goto("/assets/not-an-asset.js")).status()).toBe(404);
});

test("view-only users cannot open the schema diff", async ({ page }) => {
  await login(page, "viewer");
  expect((await page.goto(`${pathFor(engines[0])}/schema-diff`)).status()).toBe(403);
});
