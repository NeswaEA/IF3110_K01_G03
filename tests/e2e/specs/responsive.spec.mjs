import { test, expect } from "@playwright/test";
import { expectNoSideScroll, login, waitForApp } from "../helpers.mjs";

test.beforeAll(async ({ request }) => waitForApp(request));

test("signed-in pages fit the screen without sideways scrolling", async ({ page }) => {
  await login(page, "fixture-admin");
  for (const path of ["/databases", "/reviews", "/admin/users", "/admin/groups", "/admin/connections",
    "/admin/groups/20000000-0000-4000-8000-000000000001"]) {
    await page.goto(path);
    await expectNoSideScroll(page);
  }
});

test("the login page fits the screen", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("button", { name: "Masuk", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
});
