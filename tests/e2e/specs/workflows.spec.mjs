import { randomUUID } from "node:crypto";
import { test, expect } from "@playwright/test";
import {
  apiUser, blockFinalization, createDraft, engines, execute, getData, ledgerCount,
  login, metadata, mutate, origin, pathFor, ready, saveDraft,
  targetExec, targetRows, unblockFinalization, uniqueName,
} from "../helpers.mjs";

test.beforeAll(async ({ request }) => {
  await expect.poll(async () => {
    try { return (await request.get("/healthz")).status(); } catch { return 0; }
  }, { timeout: 30_000 }).toBe(200);
});

test("admin tests both source connections before migrations become ready", async ({ page }) => {
  const editor = await apiUser("editor");
  try {
    for (const engine of engines) {
      const response = await editor.get(`/api/v1${pathFor(engine)}/migration-head`);
      expect(response.status()).toBe(503);
      expect((await response.json()).error.code).toBe("TARGET_NOT_READY");
    }
  } finally { await editor.dispose(); }
  await login(page, "fixture-admin");
  await page.goto("/admin/connections");
  for (const engine of engines) {
    const row = page.getByRole("row").filter({ has: page.getByRole("cell", { name: engine.name, exact: true }) });
    await expect(row).toContainText("belum diuji");
    await row.getByRole("button", { name: "Uji koneksi", exact: true }).click();
    await expect(page.getByRole("status")).toContainText(`${engine.name}: terhubung`);
    await expect(row).toContainText("siap migrasi");
  }
  const comparisons = await metadata("SELECT migration_ready FROM database_connections WHERE id IN ($1,$2)", engines.map((e) => e.desired));
  expect(comparisons.map((row) => row.migration_ready)).toEqual([false, false]);
});

test("anonymous pages redirect while APIs, missing pages and missing assets retain their status", async ({ page, request }) => {
  await page.goto(`${pathFor(engines[0])}/migrations`);
  await expect(page).toHaveURL(/\/login\?next=/);
  expect((await request.get(`/api/v1${pathFor(engines[0])}/migration-runs`)).status()).toBe(401);
  await login(page);
  expect((await page.goto("/not-a-page")).status()).toBe(404);
  expect((await page.goto("/assets/not-an-asset.js")).status()).toBe(404);
});

for (const engine of engines) {
  test(`${engine.driver}: latest draft SQL commits DDL and DML once after a double click`, async ({ page }) => {
    await ready();
    await login(page);
    const table = uniqueName();
    const sql = `CREATE TABLE ${table} (id integer PRIMARY KEY, note text);\nINSERT INTO ${table} VALUES (1, '<script>window.e2eInjected = true</script>');`;
    const draft = await saveDraft(page, engine, "SELECT 1;", `Commit ${engine.driver}`);
    await page.getByRole("textbox", { name: "SQL migrasi" }).fill(sql);
    const saved = page.waitForResponse((r) => r.request().method() === "PATCH" && r.url().endsWith(`/migration-drafts/${draft.id}`));
    await page.getByRole("button", { name: "Simpan draft", exact: true }).click();
    expect((await saved).status()).toBe(200);
    await expect(page.getByRole("heading", { name: "Draft revision 2", exact: true })).toBeVisible();
    const { run, status } = await execute(page, engine, { double: true });
    expect(status).toBe(200);
    expect(run).toMatchObject({ status: "succeeded", sql, draft_revision: 2, actor: { username: "editor" } });
    await expect(page.getByRole("region", { name: "Detail eksekusi" })).toContainText("Status: Berhasil");
    expect(await ledgerCount(engine, run.id)).toBe(1);
    expect(Number((await targetRows(engine, `SELECT count(*) AS count FROM ${table}`))[0].count)).toBe(1);
    expect((await metadata("SELECT count(*)::int AS count FROM migration_runs WHERE draft_id=$1", [draft.id]))[0].count).toBe(1);

    await page.goto(`${pathFor(engine)}/migrations?draft=${draft.id}&run=${run.id}`);
    await page.reload();
    await expect(page.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue(sql);
    await expect(page.getByRole("textbox", { name: "SQL migrasi" })).toHaveAttribute("readonly", "");
    expect(await page.evaluate(() => window.e2eInjected)).toBeUndefined();

    const noJS = await page.context().browser().newContext({ javaScriptEnabled: false, storageState: await page.context().storageState() });
    try {
      const ssr = await noJS.newPage();
      await ssr.goto(`${origin}${pathFor(engine)}/migrations?draft=${draft.id}&run=${run.id}`);
      await expect(ssr.getByRole("region", { name: "Detail eksekusi" })).toContainText(sql);
      await expect(ssr.getByRole("region", { name: "Draft terpilih" })).toContainText(sql);
      await expect(ssr.getByRole("table")).toContainText("Berhasil");
      await ssr.reload();
      await expect(ssr.getByRole("region", { name: "Detail eksekusi" })).toContainText("editor");
    } finally { await noJS.close(); await targetExec(engine, `DROP TABLE ${table}`); }
  });

  test(`${engine.driver}: second statement failure rolls back DDL and leaves the head unchanged`, async ({ page }) => {
    await ready();
    await login(page);
    const table = uniqueName();
    const before = await getData(page.request, `${pathFor(engine)}/migration-head`);
    const draft = await saveDraft(page, engine, `CREATE TABLE ${table} (id integer); INSERT INTO ${uniqueName()} VALUES (1);`);
    const { run, status } = await execute(page, engine);
    expect(status).toBe(422);
    expect(run).toMatchObject({ status: "failed", version: null, draft_id: draft.id });
    await expect(page.getByRole("region", { name: "Detail eksekusi" })).toContainText("Status: Gagal");
    expect(await getData(page.request, `${pathFor(engine)}/migration-head`)).toEqual(before);
    const catalog = engine.driver === "postgres"
      ? `SELECT count(*) AS count FROM information_schema.tables WHERE table_schema='public' AND table_name='${table}'`
      : `SELECT count(*) AS count FROM sqlite_schema WHERE name='${table}'`;
    expect(Number((await targetRows(engine, catalog))[0].count)).toBe(0);
    expect(await ledgerCount(engine, run.id)).toBe(0);
  });

  test(`${engine.driver}: EXPLAIN receives the selected statement and leaves data unchanged`, async ({ page }) => {
    await ready();
    await login(page);
    await page.goto(`${pathFor(engine)}/migrations`);
    const sql = "SELECT id FROM items WHERE id = 2;";
    const editor = page.getByRole("textbox", { name: "SQL migrasi" });
    await editor.fill(sql + "\nDELETE FROM items;");
    await editor.press(process.platform === "darwin" ? "Meta+ArrowUp" : "Control+Home");
    await editor.press(process.platform === "darwin" ? "Meta+Shift+ArrowRight" : "Shift+End");
    const response = page.waitForResponse((r) => r.url().endsWith("/explain") && r.request().method() === "POST");
    await page.getByRole("button", { name: "Analisis rencana (EXPLAIN)", exact: true }).click();
    const result = await response;
    expect(result.request().postDataJSON()).toEqual({ sql, mode: "estimate" });
    expect(result.status()).toBe(200);
    expect((await result.json()).data.nodes.length).toBeGreaterThan(0);
    await expect(page.getByRole("region", { name: "Rencana eksekusi" })).toContainText(`Engine: ${engine.driver}`);
    expect(Number((await targetRows(engine, "SELECT count(*) AS count FROM items"))[0].count)).toBe(3);
    await editor.click();
    await editor.press(process.platform === "darwin" ? "Meta+ArrowDown" : "Control+End");
    const multiple = page.waitForResponse((r) => r.url().endsWith("/explain") && r.request().method() === "POST");
    await page.getByRole("button", { name: "Analisis rencana (EXPLAIN)", exact: true }).click();
    const rejected = await multiple;
    expect(rejected.request().postDataJSON().sql).toBe(sql + "\nDELETE FROM items;");
    expect(rejected.status()).toBe(422);
  });

  test(`${engine.driver}: real diff is reviewed, saved with its fingerprint and executed through the ordinary runner`, async ({ page }) => {
    await ready();
    await login(page);
    const before = await getData(page.request, `${pathFor(engine)}/schema`);
    const head = await getData(page.request, `${pathFor(engine)}/migration-head`);
    await page.goto(`${pathFor(engine)}/schema-diff`);
    await page.getByRole("combobox", { name: "Database pembanding" }).selectOption(engine.desired);
    await page.getByRole("combobox", { name: "Schema", exact: true }).selectOption(engine.schema);
    const compared = page.waitForResponse((r) => r.url().endsWith("/schema-diff") && r.request().method() === "POST");
    await page.getByRole("button", { name: "Bandingkan schema", exact: true }).click();
    const result = await compared;
    expect(result.status()).toBe(200);
    const diff = (await result.json()).data;
    expect(diff.executable).toBe(true);
    expect(diff.changes.length).toBeGreaterThan(0);
    expect(diff.expected_version).toBe(head.version);
    expect(diff.source_fingerprint).toBe(before.fingerprint);
    expect((await getData(page.request, `${pathFor(engine)}/schema`)).fingerprint).toBe(before.fingerprint);
    const edited = diff.candidate_sql + "\nUPDATE items SET note = 'reviewed diff' WHERE id = 2;";
    await page.getByRole("textbox", { name: "SQL migrasi" }).fill(edited);
    await page.getByRole("textbox", { name: "Judul draft" }).fill(`Diff ${engine.driver}`);
    if (engine.driver === "sqlite") {
      await page.setViewportSize({ width: 390, height: 844 });
      expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
      await page.screenshot({ path: test.info().outputPath("diff-mobile.png"), fullPage: true });
    }
    const saved = page.waitForResponse((r) => r.url().endsWith("/migration-drafts") && r.request().method() === "POST");
    await page.getByRole("button", { name: "Simpan sebagai draft", exact: true }).click();
    const response = await saved;
    expect(response.status()).toBe(201);
    expect(response.request().postDataJSON()).toEqual({ title: `Diff ${engine.driver}`, sql: edited, expected_version: diff.expected_version, source_fingerprint: diff.source_fingerprint });
    await expect(page).toHaveURL(/\/migrations\?draft=[0-9a-f-]+/);
    const id = new URL(page.url()).searchParams.get("draft");
    const draft = await getData(page.request, `${pathFor(engine)}/migration-drafts/${id}`);
    expect(draft).toMatchObject({ sql: edited, expected_version: diff.expected_version, source_fingerprint: diff.source_fingerprint });
    await expect(page).toHaveURL(new RegExp(`/migrations\\?draft=${draft.id}`));
    await page.reload();
    await expect(page.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue(edited);
    expect((await metadata("SELECT count(*)::int AS count FROM migration_runs WHERE draft_id=$1", [draft.id]))[0].count).toBe(0);
    expect(await getData(page.request, `${pathFor(engine)}/migration-head`)).toEqual(head);
    const { run } = await execute(page, engine);
    expect(run.status).toBe("succeeded");
    expect(await ledgerCount(engine, run.id)).toBe(1);
    expect((await targetRows(engine, "SELECT note,status FROM items WHERE id=2"))[0]).toMatchObject({ note: "reviewed diff", status: "active" });
    expect((await targetRows(engine, "SELECT note FROM items WHERE id=2", true))[0].note).not.toBe("reviewed diff");
    await page.goto(`${pathFor(engine)}/schema-diff`);
    const identical = page.waitForResponse((r) => r.url().endsWith("/schema-diff") && r.request().method() === "POST");
    await page.getByRole("button", { name: "Bandingkan schema", exact: true }).click();
    expect((await (await identical).json()).data.changes).toEqual([]);
    await expect(page.getByText("Schema identik. Tidak ada SQL untuk disimpan.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Simpan sebagai draft" })).toHaveCount(0);
  });
}

test("view-only users can read history, but cannot access drafts, diff or mutations", async ({ page }) => {
  await ready();
  const editor = await apiUser("editor");
  let draft;
  try { draft = await createDraft(editor, engines[0], "SELECT 1;"); } finally { await editor.dispose(); }
  await login(page, "viewer");
  await page.goto(`${pathFor(engines[0])}/migrations`);
  await expect(page.getByText("Akses lihat hanya dapat membaca riwayat migrasi.")).toBeVisible();
  await expect(page.getByRole("textbox", { name: "SQL migrasi" })).toHaveCount(0);
  expect((await page.goto(`${pathFor(engines[0])}/schema-diff`)).status()).toBe(403);
  expect((await page.request.get(`/api/v1${pathFor(engines[0])}/migration-drafts/${draft.id}`)).status()).toBe(403);
  await mutate(page.request, "POST", `${pathFor(engines[0])}/migration-drafts`, { title: "denied", sql: "SELECT 1;", expected_version: 0 }, 403);
  await mutate(page.request, "POST", `${pathFor(engines[0])}/migration-drafts/${draft.id}/execute`, { revision: draft.revision, expected_version: draft.expected_version }, 403, { "Idempotency-Key": randomUUID() });
});

test("CSRF and cross-database resources are rejected by the real backend", async ({ page }) => {
  await ready();
  await login(page);
  const draft = await createDraft(page.request, engines[0], "SELECT 1;");
  const noCSRF = await page.request.post(`/api/v1${pathFor(engines[0])}/migration-drafts`, { headers: { Origin: origin }, data: { title: "no csrf", sql: "SELECT 1;", expected_version: 0 } });
  expect(noCSRF.status()).toBe(403);
  expect((await noCSRF.json()).error.code).toBe("CSRF_REJECTED");
  expect((await page.request.get(`/api/v1${pathFor(engines[1])}/migration-drafts/${draft.id}`)).status()).toBe(404);
  await mutate(page.request, "POST", `${pathFor(engines[1])}/migration-drafts/${draft.id}/execute`, { revision: draft.revision, expected_version: draft.expected_version }, 404, { "Idempotency-Key": randomUUID() });
});

test("two editors detect stale revisions without replacing local SQL", async ({ page, context }) => {
  await ready();
  await login(page);
  const engine = engines[0];
  const draft = await createDraft(page.request, engine, "SELECT 1;");
  const other = await context.newPage();
  try {
    const path = `${pathFor(engine)}/migrations?draft=${draft.id}`;
    await page.goto(path);
    await other.goto(path);
    await page.getByRole("textbox", { name: "SQL migrasi" }).fill("SELECT 2;");
    await page.getByRole("button", { name: "Simpan draft", exact: true }).click();
    await expect(page.getByRole("heading", { name: "Draft revision 2", exact: true })).toBeVisible();
    await other.getByRole("textbox", { name: "SQL migrasi" }).fill("SELECT 3;");
    const failed = other.waitForResponse((r) => r.request().method() === "PATCH" && r.url().endsWith(`/migration-drafts/${draft.id}`));
    await other.getByRole("button", { name: "Simpan draft", exact: true }).click();
    expect((await failed).status()).toBe(409);
    await expect(other.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue("SELECT 3;");
    expect((await getData(page.request, `${pathFor(engine)}/migration-drafts/${draft.id}`)).sql).toBe("SELECT 2;");
  } finally { await other.close(); }
});

test("expired editor sessions keep SQL and require a manual resend after login", async ({ page, context }) => {
  await ready();
  await login(page);
  const engine = engines[0];
  await page.goto(`${pathFor(engine)}/migrations`);
  const title = uniqueName();
  await page.getByLabel("Judul", { exact: true }).fill(title);
  await page.getByRole("textbox", { name: "SQL migrasi" }).fill("SELECT 'keep this SQL';");
  let writes = 0;
  page.on("request", (r) => { if (r.method() === "POST" && r.url().endsWith("/migration-drafts")) writes++; });
  await mutate(page.request, "POST", "/auth/logout", {}, 204);
  const rejected = page.waitForResponse((r) => r.status() === 401 && r.url().includes("/api/v1/"));
  await page.getByRole("button", { name: "Simpan draft", exact: true }).click();
  await rejected;
  await expect(page.locator("#session-expired")).toBeVisible();
  await expect(page.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue("SELECT 'keep this SQL';");
  const next = await context.newPage();
  try { await login(next); } finally { await next.close(); }
  expect((await metadata("SELECT count(*)::int AS count FROM migration_drafts WHERE title=$1", [title]))[0].count).toBe(0);
  await expect(page.getByRole("textbox", { name: "SQL migrasi" })).toHaveValue("SELECT 'keep this SQL';");
  const saved = page.waitForResponse((r) => r.request().method() === "POST" && r.url().endsWith("/migration-drafts"));
  await page.getByRole("button", { name: "Simpan draft", exact: true }).click();
  expect((await saved).status()).toBe(201);
  expect(writes).toBeLessThanOrEqual(2);
});

test("a committed target survives metadata finalization failure and reconcile never reruns its SQL", async ({ page }) => {
  await ready();
  await login(page);
  const engine = engines[0];
  const table = uniqueName();
  const draft = await saveDraft(page, engine, `CREATE TABLE ${table} (id integer PRIMARY KEY); INSERT INTO ${table} VALUES (1);`);
  await blockFinalization(draft.id);
  try {
    const { run, status } = await execute(page, engine);
    expect(status).toBe(202);
    expect(run.status).toBe("outcome_unknown");
    await expect(page.getByRole("region", { name: "Detail eksekusi" })).toContainText("Status: Hasil belum pasti");
    expect(await ledgerCount(engine, run.id)).toBe(1);
    expect(Number((await targetRows(engine, `SELECT count(*) AS count FROM ${table}`))[0].count)).toBe(1);
    const blocked = await createDraft(page.request, engine, "SELECT 1;");
    await mutate(page.request, "POST", `${pathFor(engine)}/migration-drafts/${blocked.id}/execute`, { revision: blocked.revision, expected_version: blocked.expected_version }, 409, { "Idempotency-Key": randomUUID() });
    await unblockFinalization();
    const reconciled = page.waitForResponse((r) => r.url().endsWith(`/migration-runs/${run.id}/reconcile`) && r.request().method() === "POST");
    await page.getByRole("button", { name: "Rekonsiliasi", exact: true }).click();
    expect((await reconciled).status()).toBe(200);
    await expect(page.getByRole("region", { name: "Detail eksekusi" })).toContainText("Status: Berhasil");
    expect(await ledgerCount(engine, run.id)).toBe(1);
    expect(Number((await targetRows(engine, `SELECT count(*) AS count FROM ${table}`))[0].count)).toBe(1);
  } finally { await unblockFinalization(); await targetExec(engine, `DROP TABLE IF EXISTS ${table}`); }
});

test("a diff draft rejects an externally changed source fingerprint", async ({ page }) => {
  await ready();
  await login(page);
  const engine = engines[0];
  const table = uniqueName();
  const diff = (await mutate(page.request, "POST", `${pathFor(engine)}/schema-diff`, { desired_database_id: engine.desired, schema: engine.schema })).data;
  const draft = (await mutate(page.request, "POST", `${pathFor(engine)}/migration-drafts`, { title: uniqueName(), sql: "UPDATE items SET note = 'must not run' WHERE id=2;", expected_version: diff.expected_version, source_fingerprint: diff.source_fingerprint }, 201)).data;
  const before = (await targetRows(engine, "SELECT note FROM items WHERE id=2"))[0].note;
  await targetExec(engine, `CREATE TABLE ${table} (id integer)`);
  try {
    await page.goto(`${pathFor(engine)}/migrations?draft=${draft.id}`);
    const { run, status } = await execute(page, engine);
    expect(status).toBe(409);
    expect(run.error.code).toBe("SCHEMA_CONFLICT");
    expect(run.status).toBe("failed");
    expect(await ledgerCount(engine, run.id)).toBe(0);
    expect((await targetRows(engine, "SELECT note FROM items WHERE id=2"))[0].note).toBe(before);
  } finally { await targetExec(engine, `DROP TABLE ${table}`); }
});
