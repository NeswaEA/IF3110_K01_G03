import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { DatabaseSync } from "node:sqlite";

export function seedSQLite(file, sqlFiles) {
  const db = new DatabaseSync(file, { timeout: 5000 });
  let transaction = false;
  try {
    db.exec("PRAGMA foreign_keys = ON; BEGIN IMMEDIATE");
    transaction = true;
    const { count } = db.prepare("SELECT count(*) AS count FROM sqlite_schema WHERE name NOT LIKE 'sqlite\\_%' ESCAPE '\\'").get();
    if (count > 0) {
      db.exec("ROLLBACK");
      transaction = false;
      return false;
    }
    for (const sqlFile of sqlFiles) db.exec(fs.readFileSync(sqlFile, "utf8"));
    db.exec("COMMIT");
    transaction = false;
    return true;
  } finally {
    try {
      if (transaction) db.exec("ROLLBACK");
    } finally {
      db.close();
    }
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const [dataDir = "/data", fixtureDir = "/fixture"] = process.argv.slice(2);
  fs.mkdirSync(dataDir, { recursive: true });
  for (const [name, files] of [
    ["demo.sqlite", ["demo.sql"]],
    ["demo_desired.sqlite", ["demo.sql", "desired.sql"]],
  ]) {
    const file = path.join(dataDir, name);
    const initialized = seedSQLite(file, files.map((item) => path.join(fixtureDir, item)));
    fs.chownSync(file, 10001, 10001);
    console.log(`${name}: ${initialized ? "initialized" : "already has objects; kept as is"}`);
  }
  fs.chownSync(dataDir, 10001, 10001);
}
