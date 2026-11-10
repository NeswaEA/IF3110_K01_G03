import pg from "pg";
import { expect } from "@playwright/test";

export const origin = "http://localhost:8080";
export const password = "e2e fixture password 123";
export const engines = [
  { driver: "postgres", id: "30000000-0000-4000-8000-000000000001", desired: "30000000-0000-4000-8000-000000000003", name: "PostgreSQL demo", schema: "public" },
  { driver: "sqlite", id: "30000000-0000-4000-8000-000000000002", desired: "30000000-0000-4000-8000-000000000004", name: "SQLite demo", schema: "main" },
];
export const pathFor = (engine) => `/databases/${engine.id}`;

export async function login(page, username = "editor") {
  await page.goto("/login");
  await page.getByLabel("Username", { exact: true }).fill(username);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Masuk", exact: true }).click();
  await expect(page).toHaveURL(origin + "/databases");
}

export async function metadata(sql, params = []) {
  const client = new pg.Client("postgres://metadata_owner:e2e_metadata@metadata:5432/metadata?sslmode=disable");
  await client.connect();
  try { return (await client.query(sql, params)).rows; } finally { await client.end(); }
}
