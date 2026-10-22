import { act, fireEvent, render, screen } from "@testing-library/react";
import { useState } from "react";
import { http, HttpResponse } from "msw";
import { setupServer } from "msw/node";
import { afterAll, afterEach, beforeAll, expect, it, vi } from "vitest";
import type { MigrationRun } from "../../api/types";
import { RunDetail } from "./MigrationsPage";
import unknown from "../../../../tests/fixtures/api/migrations_run_unknown.json";

const base = "/databases/example";
const server = setupServer();
beforeAll(() => server.listen({ onUnhandledFrame: "error" }));
afterEach(() => { server.resetHandlers(); vi.useRealTimers(); });
afterAll(() => server.close());

it("stops after 30 reads and restarts only on an explicit refresh", async () => {
  let reads = 0;
  server.use(http.get(`http://localhost/api/v1${base}/migration-runs/:id`, () => { reads++; return HttpResponse.json(unknown); }));
  function Detail() {
    const [run, setRun] = useState(unknown.data as MigrationRun);
    return <RunDetail base={base} run={run} setRun={setRun} canEdit onTerminal={async () => {}} />;
  }
  vi.useFakeTimers();
  render(<Detail />);
  for (let i = 0; i < 32; i++) await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(reads).toBe(30);
  expect(screen.getByText(/Pemeriksaan otomatis berhenti/)).toBeInTheDocument();
  await act(async () => { await vi.advanceTimersByTimeAsync(60000); });
  expect(reads).toBe(30);
  fireEvent.click(screen.getByRole("button", { name: "Perbarui status" }));
  await act(async () => { await vi.advanceTimersByTimeAsync(2000); });
  expect(reads).toBe(31);
});
