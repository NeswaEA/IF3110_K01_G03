import { render, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import queued from "../../../../tests/fixtures/api/execution_queued.json";
import pending from "../../../../tests/fixtures/api/reviews_change_request_pending.json";
import type { Schemas } from "@/api/client";
import { pipelineStage, WorkflowPipeline } from "./WorkflowPipeline";

const request = pending.data as Schemas["ChangeRequest"];
const execution = queued.data as Schemas["Execution"];
const req = (status: Schemas["RequestStatus"]) => ({ ...request, status });
const exec = (status: Schemas["ExecutionStatus"]) => ({ ...execution, status });

describe("pipelineStage", () => {
  it.each([
    ["no request", undefined, undefined, 0],
    ["pending review", req("pending_review"), undefined, 1],
    ["rejected stays on review", req("rejected"), undefined, 1],
    ["cancelled stays on review", req("cancelled"), undefined, 1],
    ["approved", req("approved"), exec("cancelled"), 2],
    ["queued", req("executed"), exec("queued"), 3],
    ["held waits in the queue", req("executed"), exec("held"), 3],
    ["running", req("executed"), exec("running"), 4],
    ["succeeded", req("executed"), exec("succeeded"), 5],
    ["failed", req("executed"), exec("failed"), 5],
    ["partial", req("executed"), exec("partial"), 5],
  ] as const)("%s", (_, r, e, step) => {
    expect(pipelineStage(r, e).step).toBe(step);
  });
});

describe("WorkflowPipeline", () => {
  it("marks the current step with its status text", () => {
    render(<WorkflowPipeline request={req("executed")} execution={exec("partial")} />);
    const current = within(screen.getByRole("list", { name: "Alur migrasi" })).getAllByRole("listitem")
      .find((li) => li.getAttribute("aria-current") === "step");
    expect(current).toHaveTextContent("Selesai:Sebagian");
  });

  it("starts at draft without a request", () => {
    render(<WorkflowPipeline />);
    expect(screen.getByText("Draft").closest("li")).toHaveAttribute("aria-current", "step");
  });
});
