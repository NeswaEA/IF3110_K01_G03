import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./specs",
  workers: 1,
  retries: 0,
  forbidOnly: true,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  outputDir: "/artifacts/results",
  reporter: [["list"], ["json", { outputFile: "/artifacts/report.json" }]],
  use: {
    baseURL: "http://localhost:8080",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
  projects: [
    { name: "desktop", use: { ...devices["Desktop Chrome"], viewport: { width: 1280, height: 800 } } },
    {
      name: "mobile",
      // Runs after desktop on the same stack; tests that change fixture state run once, on desktop.
      grepInvert: /@desktop-only/,
      use: { ...devices["Desktop Chrome"], viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true },
    },
  ],
});
