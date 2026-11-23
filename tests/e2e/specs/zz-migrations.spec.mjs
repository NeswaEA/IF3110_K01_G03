import { test, expect } from "@playwright/test";
import { engines, expectNoSideScroll, login, metadata, pathFor, waitForApp } from "../helpers.mjs";

const database = pathFor(engines[0]);
const bigId = "30000000-0000-4000-8000-000000000005";
const big = `/databases/${bigId}`;

test.beforeAll(async ({ request }) => waitForApp(request));

async function post(page, path, body = {}) {
  return page.evaluate(async ({path, body}) => {
    const csrf = await fetch("/api/v1/auth/csrf");
    const {data:{csrf_token}} = await csrf.json();
    const result = await fetch("/api/v1"+path, {method:"POST",
      headers:{"Content-Type":"application/json","X-CSRF-Token":csrf_token}, body:JSON.stringify(body)});
    return {status:result.status, ...await result.json()};
  }, {path, body});
}

async function allowTarget(browser, id) {
  const admin = await browser.newPage({ baseURL: "http://localhost:8080" });
  try {
    await login(admin, "fixture-admin");
    const result = await post(admin, `/admin/connections/${id}/test`);
    expect(result.status).toBe(200);
  } finally {
    await admin.close();
  }
}

async function approve(page, path) {
  const res = await post(page,path+"/approve");
  expect(res.status).toBe(200);
  return res.data.status;
}

async function reviewThroughPanel(page, request, decision, comment) {
  const button = decision === "approve" ? "Setujui" : "Tolak";
  await page.getByRole("button", { name: button, exact: true }).click();
  const dialog = page.getByRole("dialog");
  if (comment) await dialog.getByLabel("Alasan penolakan").fill(comment);
  const [response] = await Promise.all([
    page.waitForResponse((r) => r.url().endsWith("/api/v1" + request + "/" + decision)),
    dialog.getByRole("button", { name: button, exact: true }).click(),
  ]);
  expect(response.status()).toBe(200);
  return (await response.json()).data.status;
}

async function requestFromApi(page, base, mid, action, extra = {}) {
  const res = await post(page, `${base}/migrations/${mid}/requests`, {action,...extra});
  expect(res.status).toBe(201);
  return `${base}/change-requests/${res.data.id}`;
}

async function execution(page, path) {
  const res = await post(page,path+"/execute");
  expect(res.status).toBe(202);
  return res.data.id;
}

async function waitStatus(page, base, id, status) {
  await expect.poll(async () => (await (await page.request.get(`/api/v1${base}/executions/${id}`)).json()).data?.status,
    {timeout:20000}).toBe(status);
}

test("creates, dry-runs, reviews and executes a migration through the SPA", async ({page,browser}, info) => {
  await allowTarget(browser, engines[0].id);
  await login(page, "editor");
  await page.goto(database);
  await expectNoSideScroll(page);
  const suffix=info.project.name.replace(/[^a-z]/g,"");
  const title=`Migration ${suffix}`;
  await page.goto(database+"/migrations");
  await page.getByRole("link",{name:"Buat migrasi"}).click();
  await page.getByLabel("Judul migrasi").fill(title);
  await page.getByLabel("SQL up",{exact:true}).fill(`CREATE TABLE m2_e2e_${suffix}(id integer PRIMARY KEY)`);
  await page.getByLabel("SQL down",{exact:true}).fill(`DROP TABLE m2_e2e_${suffix}`);
  await page.getByRole("button",{name:"Simpan migrasi"}).click();
  await expect(page.getByRole("heading",{name:title})).toBeVisible();
  await expectNoSideScroll(page);
  const detail=new URL(page.url()).pathname;
  const id=detail.split("/").at(-1);
  await page.getByRole("tab",{name:"Validasi"}).click();
  await page.getByRole("button",{name:"Jalankan dry-run"}).click();
  await expect(page.getByText(/Hasil passed/)).toBeVisible({timeout:15000});
  await page.getByRole("tab",{name:"Permintaan"}).click();
  await page.getByRole("button",{name:"Ajukan migrasi"}).click();
  await expect(page.getByRole("heading",{name:"Persetujuan"})).toBeVisible();
  const [{rid}]=await metadata("SELECT id rid FROM change_requests WHERE migration_id=$1", [id]);
  const path=database+"/change-requests/"+rid;

  const reviewer = await browser.newPage({baseURL:"http://localhost:8080"});
  const leader = await browser.newPage({baseURL:"http://localhost:8080"});
  try {
    await login(reviewer, "viewer");
    await login(leader, "lead");
    await reviewer.goto(detail+"?tab=requests");
    await expect(reviewer.getByRole("button",{name:"Jalankan",exact:true})).toHaveCount(0);
    expect((await post(reviewer,path+"/execute")).status).toBe(403);
    expect(await reviewThroughPanel(reviewer,path,"approve")).toBe("pending_review");
    await leader.goto(detail+"?tab=requests");
    expect(await reviewThroughPanel(leader,path,"approve")).toBe("approved");
    await page.getByRole("button",{name:"Jalankan",exact:true}).click();
    await page.getByRole("link",{name:"Lihat eksekusi",exact:true}).click();
    await expect(page.getByText("Berhasil",{exact:true})).toBeVisible({timeout:15000});
    await expectNoSideScroll(page);
    await page.goto(database+"/history");
    await expect(page.getByRole("link",{name:new RegExp(title)})).toBeVisible();
    await expectNoSideScroll(page);

    for (const action of ["rollback","forward"]) {
      const request=await requestFromApi(page,database,id,action);
      await approve(reviewer,request);
      await approve(leader,request);
      await waitStatus(page,database,await execution(page,request),"succeeded");
    }
    const brokenSQL=`INSERT INTO m2_missing_${suffix} VALUES (1)`;
    const broken=await post(page,database+"/migrations",{title:`Gagal ${suffix}`,up_sql:brokenSQL,down_sql:"SELECT 1",mode:"normal"});
    expect(broken.status).toBe(201);
    const brokenRequest=await requestFromApi(page,database,broken.data.id,"apply");
    await approve(reviewer,brokenRequest);
    await approve(leader,brokenRequest);
    await waitStatus(page,database,await execution(page,brokenRequest),"failed");
    await page.reload();
    await expect(page.getByRole("link",{name:new RegExp(title)})).toHaveCount(3);
    // Every attempt, failed or committed, is listed with the SQL it ran.
    const entry=(name)=>page.locator("ul > li").filter({has:page.getByRole("link",{name})});
    const failedEntry=entry(new RegExp(`Gagal ${suffix}`));
    await failedEntry.getByText("Riwayat attempt (1)").click();
    const failedAttempt=failedEntry.locator("ol > li").filter({hasText:"Attempt 1"});
    await expect(failedAttempt.getByText("Gagal",{exact:true})).toBeVisible();
    await expect(failedAttempt).toContainText("SQL_EXECUTION_FAILED");
    await expect(failedAttempt.getByText(brokenSQL)).toBeVisible();
    const appliedEntry=entry(new RegExp(title)).last();
    await appliedEntry.getByText("Riwayat attempt (1)").click();
    const appliedAttempt=appliedEntry.locator("ol > li").filter({hasText:"Attempt 1"});
    await expect(appliedAttempt.getByText("Berhasil",{exact:true})).toBeVisible();
    await expect(appliedAttempt.getByText(`CREATE TABLE m2_e2e_${suffix}(id integer PRIMARY KEY)`)).toBeVisible();
    await expectNoSideScroll(page);
    await page.screenshot({path:info.outputPath("history-attempts.png"),fullPage:true});
    await page.getByLabel("Sembunyikan rollback").click();
    await expect(page).toHaveURL(/hide_rollback=true/);
    await expect(page.getByRole("link",{name:new RegExp(title)})).toHaveCount(1);
    await page.reload();
    await expect(page.getByLabel("Sembunyikan rollback")).toBeChecked();
    await page.goto(detail);
    await expect(page.getByLabel("SQL up",{exact:true})).not.toBeEditable();
    await expect(page.getByRole("list",{name:"Alur migrasi"}).locator("[aria-current=step]")).toHaveText(/Selesai.*Berhasil/);
    await expectNoSideScroll(page);
  } finally {
    await Promise.all([reviewer.close(), leader.close()]);
  }
});

test("rejects a change request and preserves unsaved SQL on session expiry", async ({page,browser}) => {
  await login(page,"editor");
  const created=await post(page,database+"/migrations",{title:"Rejection and session",up_sql:"SELECT 1",down_sql:"SELECT 2",mode:"normal"});
  expect(created.status).toBe(201);
  const mid=created.data.id;
  const path=await requestFromApi(page,database,mid,"apply");
  const reviewer=await browser.newPage({baseURL:"http://localhost:8080"});
  try {
    await login(reviewer,"viewer");
    await reviewer.goto(database+"/migrations/"+mid+"?tab=requests");
    expect(await reviewThroughPanel(reviewer,path,"reject","Perlu revisi SQL.")).toBe("rejected");
    expect((await post(page,path+"/execute")).status).toBe(409);
  } finally {
    await reviewer.close();
  }
  await page.goto(database+"/migrations/"+mid);
  await page.getByLabel("SQL up",{exact:true}).fill("SELECT 999");
  // Force only the PATCH response to expire after the form is mounted.
  await page.route("**/api/v1/databases/*/migrations/"+mid,async (route)=>{
    if (route.request().method()!=="PATCH") return route.continue();
    await route.fulfill({status:401,json:{error:{code:"UNAUTHENTICATED",message:"Sesi habis",fields:{},request_id:"test"}}});
  });
  await page.getByRole("button",{name:"Simpan migrasi"}).click();
  await expect(page.getByRole("dialog")).toBeVisible();
  await expect(page.getByLabel("SQL up",{exact:true})).toHaveValue("SELECT 999");
});

test("previews a schema diff in the SQL editor and preserves an unsaved draft", async ({page,browser}) => {
  const sqlite = engines.find((engine) => engine.driver === "sqlite");
  await allowTarget(browser, sqlite.id);
  await login(page);
  const path = pathFor(sqlite);
  await page.goto(path + "/schema-diff");
  const desired = page.getByLabel("Database pembanding");
  await desired.selectOption(sqlite.desired);
  await page.getByRole("button", { name: "Bandingkan", exact: true }).click();
  const up = page.getByLabel("SQL up", { exact: true });
  await expect(up).toHaveValue(/item_tags/);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await desired.selectOption("");
  await expect(up).toHaveCount(0);

  await desired.selectOption(sqlite.desired);
  await page.getByRole("button", { name: "Bandingkan", exact: true }).click();
  await expect(up).toHaveValue(/item_tags/);
  await page.getByLabel("Judul migrasi").fill("Tambah kolom dari schema diff");
  const down = page.getByLabel("SQL down", { exact: true });
  await expect(down).toBeEditable();
  if (!(await down.inputValue())) await down.fill("SELECT 1");
  await page.getByRole("link", { name: "Riwayat", exact: true }).click();
  const confirm = page.getByRole("dialog");
  await expect(confirm.getByText("Tinggalkan perubahan?")).toBeVisible();
  await confirm.getByRole("button", { name: "Batal", exact: true }).click();
  await expect(up).toHaveValue(/item_tags/);
  await page.getByRole("button", { name: "Simpan migrasi" }).click();
  await expect(page.getByRole("heading", { name: "Tambah kolom dari schema diff" })).toBeVisible();
});

test("stops and resumes batch, then reverts the committed ranges of a failed batch @desktop-only", async ({page,browser}) => {
  await allowTarget(browser,bigId);
  await login(page,"lead");
  const query=(expression)=>`WITH b AS (SELECT id FROM public.big_items WHERE id>$1 AND id<=$2 ORDER BY id LIMIT $3) UPDATE public.big_items SET price=${expression} FROM b WHERE big_items.id=b.id RETURNING big_items.id`;
  const prepare=async (expression,pause_ms)=>{
    const res=await post(page,big+"/migrations",{title:"Batch big_items",mode:"batch",up_sql:query(expression),down_sql:query("price-1"),
      batch:{table:"big_items",schema:"public",key_column:"id",batch_size:2,pause_ms}});
    expect(res.status).toBe(201);
    const path=await requestFromApi(page,big,res.data.id,"apply");
    await approve(page,path);
    return [res.data.id,await execution(page,path)];
  };
  const [,first]=await prepare("price+1",1000);
  const detail=big+"/executions/"+first;
  await page.goto(detail);
  await expect(page.getByText(/[1-9] batch ·/)).toBeVisible();
  await page.getByRole("button",{name:"Stop setelah batch ini"}).click();
  await waitStatus(page,big,first,"partial");
  await page.getByLabel("Ukuran batch untuk resume").fill("20");
  await page.getByRole("button",{name:"Lanjutkan batch"}).click();
  await expect(page).not.toHaveURL(new RegExp(first));
  const resume=new URL(page.url()).pathname.split("/").at(-1);
  await waitStatus(page,big,resume,"succeeded");

  const [mid,failed]=await prepare("CASE WHEN big_items.id=3 THEN 1/((big_items.id-3)::numeric) ELSE price+1 END",0);
  await waitStatus(page,big,failed,"partial");
  const revert=await requestFromApi(page,big,mid,"rollback",{target_execution_id:failed});
  await approve(page,revert);
  await waitStatus(page,big,await execution(page,revert),"succeeded");
  const migration=await (await page.request.get(`/api/v1${big}/migrations/${mid}`)).json();
  expect(migration.data.status).toBe("pending");
});
