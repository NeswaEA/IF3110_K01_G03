import assert from "node:assert/strict";
import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { spawnSync } from "node:child_process";
import { test } from "node:test";
import { fileURLToPath } from "node:url";

for (const [name, stats, success] of [
  ["passing suite", { expected: 16, unexpected: 0, skipped: 0, flaky: 0 }, true],
  ["empty suite", { expected: 0, unexpected: 0, skipped: 0, flaky: 0 }, false],
  ["skipped case", { expected: 15, unexpected: 0, skipped: 1, flaky: 0 }, false],
  ["failed case", { expected: 15, unexpected: 1, skipped: 0, flaky: 0 }, false],
  ["flaky case", { expected: 15, unexpected: 0, skipped: 0, flaky: 1 }, false],
  ["missing counts", null, false],
]) {
  test(name, () => {
    const dir = mkdtempSync(join(tmpdir(), "if3110-browser-report-"));
    try {
      const report = join(dir, "report.json");
      writeFileSync(report, JSON.stringify({ stats }));
      const result = spawnSync(process.execPath, [fileURLToPath(new URL("./check-results.mjs", import.meta.url)), report]);
      assert.equal(result.status === 0, success, result.stderr.toString());
    } finally { rmSync(dir, { recursive: true }); }
  });
}
