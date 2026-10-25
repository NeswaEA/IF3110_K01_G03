export function bindDatabaseFilters(root: HTMLElement): void {
  const form = root.querySelector<HTMLFormElement>("[data-database-filters]");
  if (!form) return;

  const query = form.elements.namedItem("q") as HTMLInputElement;
  const engine = form.elements.namedItem("engine") as HTMLSelectElement;
  const access = form.elements.namedItem("access") as HTMLSelectElement;
  const rows = [...root.querySelectorAll<HTMLTableRowElement>("[data-database-row]")];
  const results = root.querySelector<HTMLElement>("[data-database-results]")!;
  const empty = root.querySelector<HTMLElement>("[data-database-filter-empty]")!;

  const update = () => {
    const needle = query.value.trim().toLocaleLowerCase("id");
    let visible = 0;
    for (const row of rows) {
      const name = (row.dataset.name ?? "").toLocaleLowerCase("id");
      row.hidden = !name.includes(needle)
        || (engine.value !== "" && row.dataset.engine !== engine.value)
        || (access.value !== "" && row.dataset.access !== access.value);
      if (!row.hidden) visible++;
    }
    results.textContent = `${visible} dari ${rows.length} database ditampilkan.`;
    empty.hidden = visible !== 0;
  };

  form.addEventListener("input", update);
  form.addEventListener("change", update);
  form.addEventListener("submit", (event) => {
    event.preventDefault();
    update();
  });
  root.querySelector<HTMLButtonElement>("[data-database-reset]")!.addEventListener("click", () => {
    form.reset();
    update();
    query.focus();
  });
  form.hidden = false;
}
