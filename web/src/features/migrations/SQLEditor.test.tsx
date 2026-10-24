import { act, fireEvent, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { StrictMode, useState } from "react";
import { expect, it, vi } from "vitest";
import type { Driver } from "../../api/types";
import { SQLEditor } from "./SQLEditor";

function open(value = "", readOnly = false) {
  const changed = vi.fn();
  const selected = vi.fn();
  function Editor() {
    const [sql, setSQL] = useState(value);
    return <>
      <button type="button">Sebelum editor</button>
      <SQLEditor value={sql} driver="postgres" readOnly={readOnly} onChange={(next) => { changed(next); setSQL(next); }} onSelection={selected} />
      <button type="button">Sesudah editor</button>
    </>;
  }
  render(<StrictMode><Editor /></StrictMode>);
  return { input: screen.getByRole("textbox", { name: "SQL migrasi" }) as HTMLTextAreaElement, changed, selected };
}

function select(input: HTMLTextAreaElement, start: number, end = start, direction: "forward" | "backward" = "forward") {
  act(() => {
    input.focus();
    input.setSelectionRange(start, end, direction);
    fireEvent.select(input);
  });
}

it("reports typed SQL and selected text without changing quoting or Unicode", async () => {
  const { input, changed, selected } = open();
  const sql = "SELECT 'teks; 😀';\nSELECT 2;";
  await userEvent.type(input, sql);
  expect(input).toHaveValue(sql);
  expect(changed).toHaveBeenLastCalledWith(sql);
  expect(selected).toHaveBeenLastCalledWith(sql);
  select(input, 7, 18, "backward");
  expect(selected).toHaveBeenLastCalledWith(sql.slice(7, 18));
  select(input, 4);
  expect(selected).toHaveBeenLastCalledWith(sql);
});

it("uses the latest external value and driver without reporting an edit", () => {
  const changed = vi.fn(), selected = vi.fn();
  const props = { value: "SELECT 1;", driver: "postgres" as Driver, readOnly: false, onChange: changed, onSelection: selected };
  const { rerender } = render(<SQLEditor {...props} />);
  const input = screen.getByRole("textbox", { name: "SQL migrasi" }) as HTMLTextAreaElement;
  select(input, 0, 6);
  rerender(<SQLEditor {...props} value="SELECT 'draft lain';" driver="sqlite" />);
  expect(input).toHaveValue("SELECT 'draft lain';");
  expect(input).toHaveAttribute("data-driver", "sqlite");
  expect(selected).toHaveBeenLastCalledWith("SELECT 'draft lain';");
  expect(changed).not.toHaveBeenCalled();
});

it("inserts two spaces at the caret and retains the scroll position", async () => {
  const { input, selected } = open("SELECT 1;");
  select(input, 6);
  input.scrollTop = 40;
  input.scrollLeft = 12;
  await userEvent.keyboard("{Tab}");
  expect(input).toHaveValue("SELECT   1;");
  expect(input.selectionStart).toBe(8);
  expect(input.selectionEnd).toBe(8);
  expect(input.scrollTop).toBe(40);
  expect(input.scrollLeft).toBe(12);
  expect(selected).toHaveBeenLastCalledWith("SELECT   1;");
});

it("indents selected lines, preserves backward selection, and reverses it with Shift+Tab", async () => {
  const sql = "SELECT 1;\nSELECT 2;\nSELECT 3;";
  const { input, selected } = open(sql);
  select(input, 2, 15, "backward");
  await userEvent.keyboard("{Tab}");
  expect(input).toHaveValue("  SELECT 1;\n  SELECT 2;\nSELECT 3;");
  expect(input.selectionStart).toBe(4);
  expect(input.selectionEnd).toBe(19);
  expect(input.selectionDirection).toBe("backward");
  expect(selected).toHaveBeenLastCalledWith(input.value.slice(4, 19));
  await userEvent.keyboard("{Shift>}{Tab}{/Shift}");
  expect(input).toHaveValue(sql);
  expect(input.selectionStart).toBe(2);
  expect(input.selectionEnd).toBe(15);
  expect(input.selectionDirection).toBe("backward");
});

it("does not indent the next line when a selection ends at its start", async () => {
  const { input } = open("SELECT 1;\nSELECT 2;");
  select(input, 0, 10);
  await userEvent.keyboard("{Tab}");
  expect(input).toHaveValue("  SELECT 1;\nSELECT 2;");
  expect(input.selectionStart).toBe(2);
  expect(input.selectionEnd).toBe(12);
});

it.each([
  { sql: "  SELECT 1;", caret: 0, expected: "SELECT 1;", next: 0 },
  { sql: "  SELECT 1;", caret: 1, expected: "SELECT 1;", next: 0 },
  { sql: " SELECT 1;", caret: 5, expected: "SELECT 1;", next: 4 },
  { sql: "\tSELECT 1;", caret: 8, expected: "SELECT 1;", next: 7 },
  { sql: "SELECT 1;", caret: 5, expected: "SELECT 1;", next: 5 },
  { sql: "\n  SELECT 1;", caret: 1, expected: "\nSELECT 1;", next: 1 },
])("outdents a line at caret $caret in '$sql' without deleting SQL", async ({ sql, caret, expected, next }) => {
  const { input } = open(sql);
  select(input, caret);
  await userEvent.keyboard("{Shift>}{Tab}{/Shift}");
  expect(input).toHaveValue(expected);
  expect(input.selectionStart).toBe(next);
  expect(input.selectionEnd).toBe(next);
});

it("outdents mixed selected lines without changing surrounding text", async () => {
  const { input } = open("before\n  SELECT 1;\n SELECT 2;\n\tSELECT 3;\nafter");
  select(input, 7, input.value.indexOf("after"));
  await userEvent.keyboard("{Shift>}{Tab}{/Shift}");
  expect(input).toHaveValue("before\nSELECT 1;\nSELECT 2;\nSELECT 3;\nafter");
  expect(input.selectionStart).toBe(7);
  expect(input.selectionEnd).toBe(input.value.indexOf("after"));
});

it("lets Escape then Tab or Shift+Tab leave the editor", async () => {
  const { input, changed } = open("SELECT 1;");
  select(input, 3);
  await userEvent.keyboard("{Escape}");
  await userEvent.tab();
  expect(screen.getByRole("button", { name: "Sesudah editor" })).toHaveFocus();
  select(input, 3);
  await userEvent.keyboard("{Escape}");
  await userEvent.tab({ shift: true });
  expect(screen.getByRole("button", { name: "Sebelum editor" })).toHaveFocus();
  expect(changed).not.toHaveBeenCalled();
});

it("only releases the next Tab and resets the escape shortcut on blur", async () => {
  const { input } = open("SELECT 1;");
  select(input, 0);
  await userEvent.keyboard("{Escape}");
  await userEvent.tab();
  select(input, 0);
  await userEvent.keyboard("{Tab}");
  expect(input).toHaveFocus();
  expect(input).toHaveValue("  SELECT 1;");
  await userEvent.keyboard("{Escape}");
  await userEvent.click(screen.getByRole("button", { name: "Sebelum editor" }));
  select(input, 0);
  await userEvent.keyboard("{Tab}");
  expect(input).toHaveValue("    SELECT 1;");
});

it("keeps read-only SQL selectable and allows normal focus navigation", async () => {
  const { input, changed, selected } = open("SELECT 1;", true);
  expect(input).toHaveAttribute("readonly");
  select(input, 0, 6);
  expect(selected).toHaveBeenLastCalledWith("SELECT");
  await userEvent.keyboard("x{Backspace}{Enter}");
  await userEvent.tab();
  expect(input).toHaveValue("SELECT 1;");
  expect(changed).not.toHaveBeenCalled();
  expect(screen.getByRole("button", { name: "Sesudah editor" })).toHaveFocus();
});

it("does not intercept browser shortcuts or composition keystrokes", () => {
  const { input, changed } = open("SELECT 1;");
  select(input, 0);
  for (const options of [{ ctrlKey: true }, { metaKey: true }, { altKey: true }, { isComposing: true }]) {
    expect(fireEvent.keyDown(input, { key: "Tab", ...options })).toBe(true);
  }
  expect(changed).not.toHaveBeenCalled();
  expect(input).toHaveValue("SELECT 1;");
});
