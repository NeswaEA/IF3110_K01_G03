import { useEffect, useRef, useState } from "react";
import { api, isAbortError, type Schemas, unwrap } from "@/api/client";
import { ErrorState } from "@/components/ErrorState";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";

type PlanNode = Schemas["PlanNode"];
type PlanResult = Schemas["PlanResult"];

export type ExplainPanelProps = { databaseId: string; sql: string; canExplain: boolean };

type State = { status: "idle" } | { status: "loading" } | { status: "success"; result: PlanResult } | { status: "error"; error: unknown };

const metric = (v: number | null) => (v === null ? "tidak tersedia" : String(v));

function NodeTree({ nodes, parent }: { nodes: PlanNode[]; parent: string | null }) {
  const children = nodes.filter((n) => n.parent_id === parent);
  if (children.length === 0) return null;
  return (
    <ul className="grid gap-2 border-l pl-4">
      {children.map((n) => (
        <li key={n.id}>
          <strong>{n.operation}</strong>
          {n.relation !== null && <> pada {n.relation}</>}
          {n.index !== null && <> memakai indeks {n.index}</>}
          <div className="text-sm text-muted-foreground">
            Estimasi baris: {metric(n.estimated_rows)}; estimasi biaya (unit engine): {metric(n.estimated_cost)}
          </div>
          <NodeTree nodes={nodes} parent={n.id} />
        </li>
      ))}
    </ul>
  );
}

/** Estimated plan of the given SQL; the statement is never executed. */
export function ExplainPanel({ databaseId, sql, canExplain }: ExplainPanelProps) {
  const [state, setState] = useState<State>({ status: "idle" });
  const inflight = useRef<AbortController | null>(null);

  // new input: drop the old result and cancel a request that describes other SQL
  useEffect(() => {
    inflight.current?.abort();
    inflight.current = null;
    setState({ status: "idle" });
  }, [databaseId, sql]);

  useEffect(() => () => inflight.current?.abort(), []);

  async function run() {
    inflight.current?.abort();
    const ctl = new AbortController();
    inflight.current = ctl;
    setState({ status: "loading" });
    try {
      const res = await unwrap(
        api.POST("/databases/{id}/explain", { params: { path: { id: databaseId } }, body: { sql, mode: "estimate" }, signal: ctl.signal }),
      );
      if (inflight.current !== ctl) return; // superseded
      setState({ status: "success", result: res.data });
    } catch (error) {
      if (isAbortError(error) || inflight.current !== ctl) return;
      setState({ status: "error", error });
    }
  }

  return (
    <section aria-label="Rencana eksekusi" className="grid gap-3">
      <Button type="button" className="h-10 justify-self-start" onClick={run} disabled={!canExplain || sql.trim() === "" || state.status === "loading"}>
        Analisis rencana (EXPLAIN)
      </Button>
      {!canExplain && <p className="text-sm text-muted-foreground">EXPLAIN memerlukan akses edit dan dukungan engine.</p>}
      {state.status === "loading" && <Skeleton className="h-24 w-full" aria-label="Meminta rencana eksekusi" />}
      {state.status === "error" && <ErrorState error={state.error} />}
      {state.status === "success" && (
        <div className="grid gap-3">
          <p className="text-sm">
            Engine: {state.result.driver}; mode: estimasi (tanpa ANALYZE, waktu aktual tidak tersedia).
          </p>
          {state.result.warnings.length > 0 && (
            <ul aria-label="Peringatan" className="list-disc pl-5 text-sm">
              {state.result.warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          )}
          <NodeTree nodes={state.result.nodes} parent={null} />
          <details>
            <summary className="cursor-pointer text-sm">Keluaran mentah engine</summary>
            <pre className="mt-2 max-h-80 overflow-auto rounded-md bg-muted p-3 text-xs">{JSON.stringify(state.result.raw, null, 2)}</pre>
          </details>
        </div>
      )}
    </section>
  );
}
