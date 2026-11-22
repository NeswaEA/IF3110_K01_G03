import { Check } from "lucide-react";
import type { Schemas } from "@/api/client";
import { StatusBadge } from "@/components/StatusBadge";
import { cn } from "@/lib/utils";

const steps = ["Draft", "Review", "Disetujui", "Antrean", "Berjalan", "Selesai"] as const;

type Current =
  | { step: number; kind: "draft" }
  | { step: number; kind: "request"; status: Schemas["RequestStatus"] }
  | { step: number; kind: "execution"; status: Schemas["ExecutionStatus"] };

/** Where the latest request and its execution sit in the workflow; branches (rejected, cancelled, partial) stay on their step. */
export function pipelineStage(request?: Schemas["ChangeRequest"], execution?: Schemas["Execution"]): Current {
  if (!request) return { step: 0, kind: "draft" };
  if (request.status !== "executed" || !execution) {
    return { step: request.status === "approved" ? 2 : 1, kind: "request", status: request.status };
  }
  const step = execution.status === "queued" || execution.status === "held" ? 3 : execution.status === "running" ? 4 : 5;
  return { step, kind: "execution", status: execution.status };
}

export function WorkflowPipeline({ request, execution }: { request?: Schemas["ChangeRequest"]; execution?: Schemas["Execution"] }) {
  const current = pipelineStage(request, execution);
  return (
    <ol aria-label="Alur migrasi" className="mb-4 grid grid-cols-2 gap-2 rounded-md border p-3 text-sm sm:grid-cols-3 lg:grid-cols-6">
      {steps.map((label, i) => (
        <li key={label} aria-current={i === current.step ? "step" : undefined}
          className={cn("flex min-w-0 flex-wrap items-center gap-2", i > current.step && "text-muted-foreground")}>
          <span className={cn("flex size-6 shrink-0 items-center justify-center rounded-full border text-xs",
            i < current.step && "border-emerald-300 bg-emerald-50 text-emerald-900", i === current.step && "border-foreground font-semibold")}>
            {i < current.step ? <Check aria-hidden className="size-3.5" /> : i + 1}
          </span>
          {i === current.step && current.kind !== "draft"
            ? <><span className="sr-only">{label}:</span><StatusBadge {...current} /></>
            : <span className={cn(i === current.step && "font-semibold")}>{label}</span>}
        </li>
      ))}
    </ol>
  );
}
