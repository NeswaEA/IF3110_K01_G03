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
    ...devices["Desktop Chrome"],
    baseURL: "http://localhost:8080",
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
  },
});
