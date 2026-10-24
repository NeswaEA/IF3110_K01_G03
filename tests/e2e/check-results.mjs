import { readFileSync } from "node:fs";

const { stats } = JSON.parse(readFileSync(process.argv[2], "utf8"));
if (!stats || stats.expected < 1 || stats.unexpected !== 0 || stats.skipped !== 0 || stats.flaky !== 0) {
  throw new Error(`Browser suite must pass without skips or retries: ${JSON.stringify(stats)}`);
}
console.log(`Browser tests: ${stats.expected} passed, 0 failed, 0 skipped.`);
