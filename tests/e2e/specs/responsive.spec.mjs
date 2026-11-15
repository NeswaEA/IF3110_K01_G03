import { test, expect } from "@playwright/test";
import { login, waitForApp } from "../helpers.mjs";

test.beforeAll(async ({ request }) => waitForApp(request));

test("signed-in pages fit the screen without sideways scrolling", async ({ page }) => {
  await login(page, "fixture-admin");
  for (const path of ["/databases", "/reviews", "/admin/users", "/admin/groups", "/admin/connections"]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { level: 1 })).toBeVisible();
    const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
    expect(overflow, `${path} scrolls sideways`).toBeLessThanOrEqual(0);
  }
});

test("the login page fits the screen", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("button", { name: "Masuk", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
});
