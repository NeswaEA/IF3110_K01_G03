import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, unwrap, type Schemas } from "@/api/client";
import { useDatabaseEvents } from "@/api/events";
import { databaseQuery } from "@/api/queries/databases";
import { batchesQuery, executionQuery, headQuery, migrationQuery } from "@/api/queries/migrations";
import { ErrorState } from "@/components/ErrorState";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge } from "@/components/StatusBadge";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { MigrationNav, PageButtons, useDatabaseMutation } from "@/features/migrations/shared";

export const Route = createFileRoute("/_app/databases/$databaseId/executions/$executionId")({
  loader: ({context,params:{databaseId,executionId}}) => Promise.all([
    context.queryClient.ensureQueryData(databaseQuery(databaseId)),
    context.queryClient.ensureQueryData(executionQuery(databaseId,executionId)),
    context.queryClient.ensureQueryData(headQuery(databaseId)),
  ]),
  component: ExecutionPage,
});

function ExecutionPage() {
  const {databaseId:id,executionId:eid}=Route.useParams();
  const navigate=Route.useNavigate();
  const {data: db}=useSuspenseQuery(databaseQuery(id));
  const {data:e}=useSuspenseQuery(executionQuery(id,eid));
  const {data:head}=useSuspenseQuery(headQuery(id));
  const [size,setSize]=useState("");
  const params={path:{id,eid}};
  useDatabaseEvents(id);
  const cancel=useDatabaseMutation(id,()=>unwrap(api.POST("/databases/{id}/executions/{eid}/cancel",{params,body:{}})));
  const stop=useDatabaseMutation(id,()=>unwrap(api.POST("/databases/{id}/executions/{eid}/stop",{params,body:{}})));
  const reconcile=useDatabaseMutation(id,()=>unwrap(api.POST("/databases/{id}/executions/{eid}/reconcile",{params,body:{}})));
  const resume=useDatabaseMutation(id,()=>unwrap(api.POST("/databases/{id}/executions/{eid}/resume",
    {params,body:{batch_size:size ? Number(size) : undefined}})));
  const pending=cancel.isPending || stop.isPending || reconcile.isPending || resume.isPending;
  const error=cancel.error ?? stop.error ?? reconcile.error ?? resume.error;
  const traceHref=e.trace_id ? traceURL(e.trace_id) : null;
  return <>
    <MigrationNav databaseId={id}/>
    <PageHeader title={`E${e.number} · ${e.migration_title}`} description={`${e.action} · ${e.mode} · Revisi ${e.migration_revision}`}
      actions={<StatusBadge kind="execution" status={e.status}/>}/>
    <div className="grid min-w-0 gap-4">
      <Link className="underline" to="/databases/$databaseId/migrations/$migrationId"
        params={{databaseId:id,migrationId:e.migration_id}} search={{tab:"requests"}}>Buka permintaan migrasi</Link>
      <dl className="grid gap-3 rounded-lg border p-4 sm:grid-cols-2">
        <div><dt className="text-sm text-muted-foreground">Pengaju</dt><dd>{e.actor.username}</dd></div>
        <div><dt className="text-sm text-muted-foreground">Percobaan</dt><dd>{e.attempt} / {e.max_attempts}</dd></div>
        {e.held_reason && <div><dt>Alasan ditahan</dt><dd>{e.held_reason}</dd></div>}
        {e.trace_id && traceHref && <div className="min-w-0"><dt>Trace</dt><dd className="break-all">
          <a className="underline" href={traceHref} target="_blank" rel="noreferrer">{e.trace_id}</a></dd></div>}
      </dl>
      {e.error && <p className="break-words rounded-md border border-destructive p-3" role="alert">{e.error.code} · {e.error.message}</p>}
      {e.dry_run && <div className="rounded-md border p-3">
        <h2 className="font-semibold">Dry-run · {e.dry_run.status}</h2>
        {e.dry_run.message && <p className="break-words">{e.dry_run.message}</p>}
        {e.dry_run.reversible === false && <p>SQL down belum memulihkan kondisi awal.</p>}
        <ul>{e.dry_run.schema_changes.map((change,i) => <li className="break-words" key={i}>{change.kind} · {change.object}</li>)}</ul>
      </div>}
      {error && <ErrorState error={error}/>}
      {db.effective_access === "edit" && <div className="flex flex-wrap items-end gap-2">
        {["queued","held"].includes(e.status) && !e.batch?.batches_committed && <Button variant="outline" disabled={pending} onClick={() => cancel.mutate()}>Batalkan eksekusi</Button>}
        {e.status === "running" && e.batch && e.action !== "dry_run" && <Button variant="outline" disabled={pending || e.batch.stop_requested}
          onClick={() => stop.mutate()}>{e.batch.stop_requested ? "Stop diminta" : "Stop setelah batch ini"}</Button>}
        {e.status === "outcome_unknown" && <Button disabled={pending} onClick={() => reconcile.mutate()}>Reconcile</Button>}
        {head.blocking_execution_id === e.id && (e.status === "partial" || e.status === "partially_reverted") && <>
          <label className="grid gap-1 text-sm">Ukuran batch untuk resume
            <Input type="number" min={1} value={size} onChange={(event) => setSize(event.target.value)} />
          </label>
          <Button disabled={pending || !!size && Number(size)<1} onClick={() => resume.mutate(undefined,{
            onSuccess:({data})=>void navigate({to:"/databases/$databaseId/executions/$executionId",params:{databaseId:id,executionId:data.id}}),
          })}>Lanjutkan batch</Button>
          {e.status === "partial" && e.direction === "up" && <RollbackLink execution={e}/>}
        </>}
      </div>}
      {e.batch && e.action !== "dry_run" && <BatchProgress execution={e}/>}
    </div>
  </>;
}

function traceURL(traceId: string) {
  const datasource = { type: "tempo", uid: "tempo" };
  const panes = {
    trace: {
      datasource: datasource.uid,
      queries: [{ refId: "A", datasource, queryType: "traceql", query: traceId }],
      range: { from: "now-7d", to: "now" },
    },
  };
  const query = new URLSearchParams({ orgId: "1", schemaVersion: "1", panes: JSON.stringify(panes) });
  const grafana = import.meta.env.VITE_GRAFANA_URL || "http://localhost:3000";
  return `${grafana.replace(/\/$/, "")}/explore?${query}`;
}

function RollbackLink({execution:e}: {execution:Schemas["Execution"]}) {
  const {data:migration}=useSuspenseQuery(migrationQuery(e.database_id,e.migration_id));
  if (!migration.current.down_sql) return null;
  return <Link className="underline" to="/databases/$databaseId/migrations/$migrationId"
    params={{databaseId:e.database_id,migrationId:e.migration_id}} search={{tab:"requests"}}>Ajukan rollback batch</Link>;
}

function BatchProgress({execution:e}: {execution:Schemas["Execution"]}) {
  const [offset,setOffset]=useState(0);
  const {data}=useSuspenseQuery(batchesQuery(e.database_id,e.id,offset,e.status === "running" || e.status === "queued"));
  if (!e.batch) return null;
  return <section className="grid min-w-0 gap-3">
    <h2 className="text-lg font-semibold">Progres batch</h2>
    <p>{e.batch.batches_committed} batch · {e.batch.rows_committed} baris committed · Cursor {e.batch.cursor ?? "—"}</p>
    {e.batch.estimated_batches !== null && <p className="text-sm text-muted-foreground">Estimasi {e.batch.estimated_batches} batch</p>}
    <div className="max-w-full overflow-auto rounded-lg border">
      <table className="w-full text-sm">
        <thead><tr><th className="p-2">Batch</th><th className="p-2">Dari</th><th className="p-2">Sampai</th><th className="p-2">Baris</th></tr></thead>
        <tbody>{data.data.map((b)=><tr key={b.batch_no} className="border-t text-center">
          <td className="p-2">{b.batch_no}</td><td>{b.cursor_from}</td><td>{b.cursor_to}</td><td>{b.rows}</td>
        </tr>)}</tbody>
      </table>
    </div>
    {(offset > 0 || data.page.has_more) && <PageButtons page={data.page} onOffset={setOffset}/>}
  </section>;
}
