// EXPLAIN panel. Owned by samson; the migration
// editor (argha) mounts it with one selected statement. It requests a plan only
// on click, clears the result when the input changes, and ignores late responses.
import { useEffect, useRef, useState } from "react";
import { isAbortError, request } from "../../api/client";
import type { PlanNode, PlanResult } from "../../api/types";
import { ErrorState, Loading } from "../../components/StatusViews";

export type ExplainPanelProps = {
  databaseId: string;
  sql: string;
  canExplain: boolean;
};

type State =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "success"; result: PlanResult }
  | { status: "error"; error: unknown };

const metric = (v: number | null) => (v === null ? "tidak tersedia" : String(v));

function NodeTree({ nodes, parent }: { nodes: PlanNode[]; parent: string | null }) {
  const children = nodes.filter((n) => n.parent_id === parent);
  if (children.length === 0) return null;
  return (
    <ul>
      {children.map((n) => (
        <li key={n.id}>
          <strong>{n.operation}</strong>
          {n.relation !== null && <> pada {n.relation}</>}
          {n.index !== null && <> memakai indeks {n.index}</>}
          <div>
            Estimasi baris: {metric(n.estimated_rows)}; estimasi biaya (unit engine): {metric(n.estimated_cost)}
          </div>
          <NodeTree nodes={nodes} parent={n.id} />
        </li>
      ))}
    </ul>
  );
}

export function ExplainPanel({ databaseId, sql, canExplain }: ExplainPanelProps) {
  const [state, setState] = useState<State>({ status: "idle" });
  const inflight = useRef<AbortController | null>(null);

  // new input: drop the old result and cancel a request that would describe other SQL
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
      const res = await request<PlanResult>(`/databases/${encodeURIComponent(databaseId)}/explain`, {
        method: "POST",
        body: { sql, mode: "estimate" },
        signal: ctl.signal,
      });
      if (inflight.current !== ctl) return; // superseded
      setState({ status: "success", result: res!.data });
    } catch (error) {
      if (isAbortError(error) || inflight.current !== ctl) return;
      setState({ status: "error", error });
    }
  }

  const disabled = !canExplain || sql.trim() === "" || state.status === "loading";
  return (
    <section aria-label="Rencana eksekusi">
      <button type="button" onClick={run} disabled={disabled}>
        Analisis rencana (EXPLAIN)
      </button>
      {!canExplain && <p>EXPLAIN memerlukan akses edit dan dukungan engine.</p>}
      {state.status === "loading" && <Loading label="Meminta rencana eksekusi..." />}
      {state.status === "error" && <ErrorState error={state.error} />}
      {state.status === "success" && (
        <div>
          <p>
            Engine: {state.result.driver}; mode: estimasi (tanpa ANALYZE, waktu aktual tidak tersedia).
          </p>
          {state.result.warnings.length > 0 && (
            <ul aria-label="Peringatan">
              {state.result.warnings.map((w) => (
                <li key={w}>{w}</li>
              ))}
            </ul>
          )}
          <NodeTree nodes={state.result.nodes} parent={null} />
          <details>
            <summary>Keluaran mentah engine</summary>
            <pre>{JSON.stringify(state.result.raw, null, 2)}</pre>
          </details>
        </div>
      )}
    </section>
  );
}

export default ExplainPanel;
