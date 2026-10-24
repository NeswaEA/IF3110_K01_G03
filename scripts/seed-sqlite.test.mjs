import assert from "node:assert/strict";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";
import { test } from "node:test";
import { seedSQLite } from "./seed-sqlite.mjs";

const fixtures = fileURLToPath(new URL("../tests/fixtures/sqlite/", import.meta.url));

function fileFor(t) {
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), "if3110-fixture-"));
  t.after(() => fs.rmSync(dir, { recursive: true, force: true }));
  return path.join(dir, "target.sqlite");
}

test("source and desired have complete fixtures and survive a rerun", (t) => {
  for (const desired of [false, true]) {
    const file = fileFor(t);
    const files = [path.join(fixtures, "demo.sql")];
    if (desired) files.push(path.join(fixtures, "desired.sql"));
    assert.equal(seedSQLite(file, files), true);
    assert.equal(seedSQLite(file, files), false);
    const db = new DatabaseSync(file);
    try {
      assert.equal(db.prepare("SELECT count(*) AS n FROM items").get().n, 3);
      assert.equal(db.prepare("SELECT count(*) AS n FROM sqlite_schema WHERE name='item_tags'").get().n, desired ? 1 : 0);
      assert.equal(db.prepare("SELECT count(*) AS n FROM pragma_table_info('items') WHERE name='status'").get().n, desired ? 1 : 0);
      assert.deepEqual(db.prepare("PRAGMA foreign_key_check").all(), []);
    } finally {
      db.close();
    }
  }
});

test("an existing empty file is initialized", (t) => {
  const file = fileFor(t);
  fs.writeFileSync(file, "");
  assert.equal(seedSQLite(file, [path.join(fixtures, "demo.sql"), path.join(fixtures, "desired.sql")]), true);
});

test("existing data and ledger are kept", (t) => {
  const file = fileFor(t);
  let db = new DatabaseSync(file);
  db.exec("CREATE TABLE mine(value TEXT); INSERT INTO mine VALUES('keep'); CREATE TABLE _platform_migration_ledger(version INTEGER); INSERT INTO _platform_migration_ledger VALUES(7)");
  const before = db.prepare("SELECT name,sql FROM sqlite_schema ORDER BY name").all();
  db.close();
  assert.equal(seedSQLite(file, [path.join(fixtures, "demo.sql"), path.join(fixtures, "desired.sql")]), false);
  db = new DatabaseSync(file);
  try {
    assert.deepEqual(db.prepare("SELECT name,sql FROM sqlite_schema ORDER BY name").all(), before);
    assert.equal(db.prepare("SELECT value FROM mine").get().value, "keep");
    assert.equal(db.prepare("SELECT version FROM _platform_migration_ledger").get().version, 7);
  } finally {
    db.close();
  }
});

test("a failed fixture rolls back completely and can be retried", (t) => {
  const file = fileFor(t);
  const invalid = path.join(path.dirname(file), "invalid.sql");
  fs.writeFileSync(invalid, "CREATE TABLE partial(id INTEGER); INSERT INTO missing VALUES(1)");
  assert.throws(() => seedSQLite(file, [invalid]));
  const db = new DatabaseSync(file);
  try {
    assert.equal(db.prepare("SELECT count(*) AS n FROM sqlite_schema").get().n, 0);
  } finally {
    db.close();
  }
  assert.equal(seedSQLite(file, [path.join(fixtures, "demo.sql")]), true);
});
