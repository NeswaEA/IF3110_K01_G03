import { fireEvent, screen } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it } from "vitest";
import databases from "../../../../tests/fixtures/api/databases_list.json";
import { bindDatabaseFilters } from "./databases";

let root: HTMLElement;

beforeEach(() => {
  document.body.innerHTML = `<section data-database-list>
    <form data-database-filters role="search" aria-label="Filter database" hidden>
      <label for="q">Cari database</label><input id="q" name="q" type="search">
      <label for="engine">Engine</label><select id="engine" name="engine"><option value="">Semua engine</option><option value="postgres">PostgreSQL</option><option value="sqlite">SQLite</option></select>
      <label for="access">Hak akses</label><select id="access" name="access"><option value="">Semua akses</option><option value="edit">Lihat dan edit</option><option value="view">Hanya lihat</option></select>
    </form>
    <table><tbody></tbody></table><p data-database-results role="status">2 dari 2 database ditampilkan.</p>
    <section data-database-filter-empty hidden><h2>Database tidak ditemukan</h2><button data-database-reset type="button">Hapus filter</button></section>
  </section>`;
  root = document.querySelector<HTMLElement>("[data-database-list]")!;
  for (const database of databases.data) {
    const row = document.createElement("tr");
    row.setAttribute("data-database-row", "");
    row.dataset.name = database.name;
    row.dataset.engine = database.driver;
    row.dataset.access = database.effective_access;
    const cell = document.createElement("td");
    cell.textContent = database.name;
    row.append(cell);
    root.querySelector("tbody")!.append(row);
  }
});

function visibleNames(): string[] {
  return [...root.querySelectorAll<HTMLTableRowElement>("[data-database-row]")].filter((row) => !row.hidden).map((row) => row.dataset.name!);
}

describe("database filters", () => {
  it("reveals controls while preserving the server-rendered initial list", () => {
    expect(root.querySelector("form")).not.toBeVisible();
    bindDatabaseFilters(root);
    expect(screen.getByRole("search")).toBeVisible();
    expect(visibleNames()).toEqual(databases.data.map((database) => database.name));
  });

  it("matches a trimmed query without changing the target's name", async () => {
    bindDatabaseFilters(root);
    await userEvent.type(screen.getByRole("searchbox"), "  POSTGRESQL  ");
    expect(visibleNames()).toEqual(["PostgreSQL latihan"]);
    expect(screen.getByRole("status")).toHaveTextContent("1 dari 2 database ditampilkan.");
  });

  it("combines engine and permission filters and resets an empty result", async () => {
    bindDatabaseFilters(root);
    const user = userEvent.setup();
    await user.selectOptions(screen.getByLabelText("Engine"), "sqlite");
    expect(visibleNames()).toEqual(["SQLite latihan"]);
    await user.selectOptions(screen.getByLabelText("Hak akses"), "edit");
    expect(visibleNames()).toEqual([]);
    expect(screen.getByRole("heading", { name: "Database tidak ditemukan" })).toBeVisible();
    expect(screen.getByRole("status")).toHaveTextContent("0 dari 2 database ditampilkan.");
    await user.click(screen.getByRole("button", { name: "Hapus filter" }));
    expect(visibleNames()).toHaveLength(2);
    expect(screen.getByLabelText("Engine")).toHaveValue("");
    expect(screen.getByLabelText("Hak akses")).toHaveValue("");
    expect(screen.queryByRole("heading", { name: "Database tidak ditemukan" })).not.toBeInTheDocument();
    expect(screen.getByRole("searchbox")).toHaveFocus();
  });

  it("keeps a submitted search on the current document", () => {
    bindDatabaseFilters(root);
    const query = screen.getByRole("searchbox") as HTMLInputElement;
    query.value = "SQLite";
    expect(fireEvent.submit(root.querySelector("form")!)).toBe(false);
    expect(visibleNames()).toEqual(["SQLite latihan"]);
  });

  it("treats names containing markup as text", async () => {
    const row = root.querySelector<HTMLTableRowElement>("[data-database-row]")!;
    row.dataset.name = '<img src=x onerror=alert(1)>';
    row.querySelector("td")!.textContent = row.dataset.name;
    bindDatabaseFilters(root);
    await userEvent.type(screen.getByRole("searchbox"), "<img");
    expect(visibleNames()).toEqual(['<img src=x onerror=alert(1)>']);
    expect(root.querySelector("img")).toBeNull();
  });

  it("leaves the original empty state alone when no filters exist", () => {
    root.innerHTML = "<p>Belum ada database yang dapat Anda akses.</p>";
    expect(() => bindDatabaseFilters(root)).not.toThrow();
    expect(root).toHaveTextContent("Belum ada database yang dapat Anda akses.");
  });
});
