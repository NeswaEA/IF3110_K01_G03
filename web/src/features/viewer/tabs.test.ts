import { fireEvent, screen } from "@testing-library/dom";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { bindTabs } from "./tabs";

const markup = `<nav data-tabs aria-label="Bagian tabel"><a href="#kolom">Kolom</a><a href="#data">Data</a></nav>
<section id="kolom"><h3>Kolom</h3></section><section id="data"><h3>Data</h3></section>`;

beforeEach(() => { document.body.innerHTML = markup; });
afterEach(() => {
  document.body.innerHTML = "";
  history.replaceState(null, "", "/");
});

describe("table tabs", () => {
  it("shows the first panel, switches on click, and remembers the tab in the hash", async () => {
    bindTabs(document.querySelector("nav")!);
    expect(screen.getByRole("tab", { name: "Kolom" })).toHaveAttribute("aria-selected", "true");
    expect(document.getElementById("data")).not.toBeVisible();
    await userEvent.click(screen.getByRole("tab", { name: "Data" }));
    expect(document.getElementById("data")).toBeVisible();
    expect(document.getElementById("kolom")).not.toBeVisible();
    expect(location.hash).toBe("#data");
  });

  it("opens the tab named by the hash and moves with arrow keys", () => {
    history.replaceState(null, "", "#data");
    bindTabs(document.querySelector("nav")!);
    const data = screen.getByRole("tab", { name: "Data" });
    expect(data).toHaveAttribute("aria-selected", "true");
    fireEvent.keyDown(data, { key: "ArrowRight" });
    expect(screen.getByRole("tab", { name: "Kolom" })).toHaveFocus();
    expect(screen.getByRole("tabpanel", { name: "Kolom" })).toBeVisible();
  });
});
