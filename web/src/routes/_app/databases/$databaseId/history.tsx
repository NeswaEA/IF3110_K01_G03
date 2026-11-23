import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useDatabaseEvents } from "@/api/events";
import { headQuery, historyQuery } from "@/api/queries/migrations";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge } from "@/components/StatusBadge";
import { MigrationNav, PageButtons } from "@/features/migrations/shared";

export const Route = createFileRoute("/_app/databases/$databaseId/history")({
  validateSearch: (search):{ hide_rollback?: boolean; offset?: number } => ({
    hide_rollback: search.hide_rollback === true ? true : undefined,
    offset: Number.isInteger(search.offset) && Number(search.offset) > 0 ? Number(search.offset) : undefined,
  }),
  loaderDeps: ({search}) => search,
  loader: ({context, params, deps}) => Promise.all([
    context.queryClient.ensureQueryData(headQuery(params.databaseId)),
    context.queryClient.ensureQueryData(historyQuery(params.databaseId, deps.hide_rollback ?? false, deps.offset ?? 0)),
  ]),
  component: HistoryPage,
});

function HistoryPage() {
  const {databaseId} = Route.useParams();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const {data: head} = useSuspenseQuery(headQuery(databaseId));
  const {data} = useSuspenseQuery(historyQuery(databaseId, search.hide_rollback ?? false, search.offset ?? 0));
  useDatabaseEvents(databaseId);
  return <>
    <MigrationNav databaseId={databaseId}/>
    <PageHeader title="Riwayat migrasi" description={`Versi aktif ${head.version}${head.blocked ? " · Database terblokir" : ""}`}/>
    {head.blocking_execution_id && <Link className="mb-4 inline-block underline" to="/databases/$databaseId/executions/$executionId"
      params={{databaseId,executionId:head.blocking_execution_id}}>Lihat eksekusi yang memblokir</Link>}
    <label className="mb-4 flex items-center gap-2">
      <input type="checkbox" checked={search.hide_rollback ?? false}
        onChange={(e) => void navigate({search:{hide_rollback:e.target.checked || undefined}})}/>
      Sembunyikan rollback
    </label>
    {data.data.length === 0 && <p>Belum ada riwayat eksekusi.</p>}
    <ul className="mb-4 grid min-w-0 gap-3">
      {data.data.map(({execution:e,committed,attempts}) => <li key={e.id} className="min-w-0 rounded-lg border p-4">
        <div className="flex flex-wrap items-center gap-3">
          <Link className="break-words font-semibold underline-offset-4 hover:underline" to="/databases/$databaseId/executions/$executionId"
            params={{databaseId,executionId:e.id}}>E{e.number} · {e.migration_title}</Link>
          <StatusBadge kind="execution" status={e.status}/>
        </div>
        <p className="mt-2 break-words text-sm text-muted-foreground">
          {e.action} · {e.actor.username} · {new Date(e.finished_at ?? e.enqueued_at).toLocaleString("id-ID")}
          {committed ? ` · Versi ${committed.version}` : ""}
        </p>
        {e.error && <p className="mt-2 break-words text-sm">{e.error.code} · {e.error.message}</p>}
        {committed && <details className="mt-3 min-w-0"><summary>Lihat SQL committed</summary>
          <pre className="mt-2 max-h-64 overflow-auto rounded-md bg-muted p-3 text-sm">{committed.sql_executed}</pre>
        </details>}
        {attempts.length > 0 && <details className="mt-3 min-w-0"><summary>Riwayat attempt ({attempts.length})</summary>
          <ol className="mt-2 grid min-w-0 gap-3">
            {attempts.map((a, i) => <li key={i} className="min-w-0 rounded-md border p-3 text-sm">
              <div className="flex flex-wrap items-center gap-2">
                <span className="font-medium">Attempt {a.attempt}</span>
                <StatusBadge kind="execution" status={a.status}/>
                {a.retry_scheduled && <span className="text-muted-foreground">dijadwalkan ulang</span>}
              </div>
              <p className="mt-1 break-words text-muted-foreground">
                {a.actor.username} · {new Date(a.recorded_at).toLocaleString("id-ID")} · Revisi {a.migration_revision}
                {a.error ? ` · ${a.error.code} · ${a.error.message}` : ""}
              </p>
              {a.sql_executed === null
                ? <p className="mt-1 text-muted-foreground">SQL belum dijalankan pada attempt ini.</p>
                : <pre className="mt-2 max-h-48 overflow-auto rounded-md bg-muted p-3">{a.sql_executed}</pre>}
            </li>)}
          </ol>
        </details>}
      </li>)}
    </ul>
    {(data.page.has_more || data.page.offset > 0) && <PageButtons page={data.page} onOffset={(offset) => void navigate({search:{...search,offset}})}/>}
  </>;
}
