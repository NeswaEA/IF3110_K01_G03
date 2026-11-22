import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { api, unwrap, type Schemas } from "@/api/client";
import { useDatabaseEvents } from "@/api/events";
import { databaseQuery } from "@/api/queries/databases";
import { executionsQuery, headQuery, migrationQuery, requestsQuery, revisionsQuery } from "@/api/queries/migrations";
import { ErrorState } from "@/components/ErrorState";
import { PageHeader } from "@/components/PageHeader";
import { SqlDiffView } from "@/components/SqlDiffView";
import { StatusBadge } from "@/components/StatusBadge";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { MigrationForm } from "@/features/migrations/MigrationForm";
import { MigrationNav, useDatabaseMutation } from "@/features/migrations/shared";
import { WorkflowPipeline } from "@/features/migrations/WorkflowPipeline";
import { ApprovalPanel } from "@/features/reviews/ApprovalPanel";

type Tab = "sql" | "validation" | "revisions" | "requests";

export const Route = createFileRoute("/_app/databases/$databaseId/migrations/$migrationId")({
  validateSearch: (search): { tab?: Tab } => ({
    tab: search.tab === "validation" || search.tab === "revisions" || search.tab === "requests" ? search.tab : undefined,
  }),
  loader: ({context, params: {databaseId, migrationId}}) => Promise.all([
    context.queryClient.ensureQueryData(databaseQuery(databaseId)),
    context.queryClient.ensureQueryData(migrationQuery(databaseId, migrationId)),
  ]),
  component: MigrationDetail,
});

function MigrationDetail() {
  const {databaseId,migrationId} = Route.useParams();
  const {tab = "sql"} = Route.useSearch();
  const navigate = Route.useNavigate();
  const {data: db} = useSuspenseQuery(databaseQuery(databaseId));
  const {data: m} = useSuspenseQuery(migrationQuery(databaseId,migrationId));
  const editable = db.effective_access === "edit";
  const save = useDatabaseMutation(databaseId, (body: Schemas["PatchMigrationInput"]) =>
    unwrap(api.PATCH("/databases/{id}/migrations/{mid}", {params:{path:{id:databaseId, mid:migrationId}}, body})));
  useDatabaseEvents(databaseId);
  return <>
    <MigrationNav databaseId={databaseId}/>
    <PageHeader title={m.title}
      description={`Revisi ${m.current.revision}${m.locked ? " · SQL terkunci" : ""}`}/>
    <WorkflowStatus migration={m}/>
    <Tabs value={tab} onValueChange={(tab) => void navigate({search:{tab:tab as Tab}, ignoreBlocker: false})}>
      <TabsList className="mb-4 flex h-auto w-full flex-wrap justify-start">
        <TabsTrigger value="sql">SQL</TabsTrigger><TabsTrigger value="validation">Validasi</TabsTrigger>
        <TabsTrigger value="revisions">Revisi</TabsTrigger><TabsTrigger value="requests">Permintaan</TabsTrigger>
      </TabsList>
      <TabsContent value="sql">
        <MigrationForm key={save.data?.data.updated_at ?? migrationId} database={db} migration={m}
          pending={save.isPending} error={save.error}
          onSave={(draft, revision) => {
            const body: Schemas["PatchMigrationInput"] = m.locked
              ? {title:draft.title,description:draft.description,revision:revision ?? m.current.revision}
              : { ...draft, down_sql: draft.down_sql ?? null, irreversible_reason: draft.irreversible_reason ?? null, revision:revision ?? m.current.revision };
            save.mutate(body);
          }}/>
      </TabsContent>
      <TabsContent value="validation"><Validation migration={m} canDryRun={editable && db.capabilities.dry_run}/></TabsContent>
      <TabsContent value="revisions"><Revisions migration={m}/></TabsContent>
      <TabsContent value="requests"><Requests migration={m} editable={editable}/></TabsContent>
    </Tabs>
  </>;
}

function WorkflowStatus({migration:m}: {migration:Schemas["Migration"]}) {
  const {data:requests}=useSuspenseQuery(requestsQuery(m.database_id,m.id));
  const {data:executions}=useSuspenseQuery(executionsQuery(m.database_id,m.id));
  const request=m.open_request ?? requests[0];
  const execution=executions.find((e) => e.action !== "dry_run");
  return <WorkflowPipeline request={request} execution={execution}/>;
}

function Validation({migration: m, canDryRun}: {migration: Schemas["Migration"]; canDryRun: boolean}) {
  const id = m.database_id;
  const mutation = useDatabaseMutation(id, () => unwrap(api.POST("/databases/{id}/migrations/{mid}/dry-run",
    {params:{path:{id,mid:m.id}},body:{}})));
  const r = m.latest_dry_run;
  return <section className="grid gap-4">
    <h2 className="text-lg font-semibold">Temuan lint</h2>
    {m.current.lint.length === 0 ? <p>Tidak ada temuan.</p> : <ul className="grid gap-2">
      {m.current.lint.map((f, key) => <li key={key} className="rounded-md border p-3 text-sm">
        <strong>{f.severity} · {f.category} · {f.direction}{f.statement_index ? ` #${f.statement_index}` : ""}</strong>
        <p>{f.message}</p>
      </li>)}</ul>}
    <h2 className="text-lg font-semibold">Dry-run</h2>
    {canDryRun && <Button type="button" className="justify-self-start" disabled={mutation.isPending} onClick={() => mutation.mutate()}>Jalankan dry-run</Button>}
    {mutation.error && <ErrorState error={mutation.error}/>}
    {mutation.data && <Link className="underline" to="/databases/$databaseId/executions/$executionId" params={{databaseId:id,executionId:mutation.data.data.id}}>Lihat eksekusi dry-run</Link>}
    {r && <div className="rounded-md border p-3">
      <p>Hasil {r.status} · {Math.round(r.duration_ms)} ms</p>
      {r.message && <p className="break-words">{r.message}</p>}
      {r.reversible === false && <p>SQL down belum memulihkan kondisi awal.</p>}
      <ul>{r.schema_changes.map((c, i) => <li className="break-words" key={i}>{c.kind} · {c.object}</li>)}</ul>
    </div>}
  </section>;
}

function Revisions({migration: m}: {migration: Schemas["Migration"]}) {
  const {data: revisions} = useSuspenseQuery(revisionsQuery(m.database_id,m.id));
  const [selected, setSelected] = useState<number>();
  const before = revisions.find((r) => r.revision === selected) ?? revisions[1] ?? m.current;
  return <div className="grid min-w-0 gap-3">
    <label className="grid max-w-xs gap-1 text-sm">Bandingkan revisi
      <select className="rounded-md border p-2" value={before.revision} onChange={(e) => setSelected(Number(e.target.value))}>
        {revisions.map((r) => <option key={r.revision} value={r.revision}>Revisi {r.revision}</option>)}
      </select>
    </label>
    <SqlDiffView before={before} after={m.current}/>
  </div>;
}

function Requests({migration:m,editable}: {migration: Schemas["Migration"]; editable:boolean}) {
  const id = m.database_id;
  const {data:requests} = useSuspenseQuery(requestsQuery(id,m.id));
  const {data:head} = useSuspenseQuery(headQuery(id));
  const {data:execs} = useSuspenseQuery(executionsQuery(id,m.id));
  const [batchSize, setBatchSize] = useState("");
  const open = m.open_request;
  const active = execs.some((e) => e.action !== "dry_run" && ["queued","held","running"].includes(e.status));
  const partial = execs.find((e) => e.id === head.blocking_execution_id && e.status === "partial" && e.direction === "up");
  const action = m.status === "pending" ? "apply" : (m.current.down_sql && (head.migration_id === m.id && m.status === "applied" || partial) ? "rollback" :
    m.status === "rolled_back" && m.version === head.version + 1 ? "forward" : null);
  const submit = useDatabaseMutation(id, (action: Schemas["RequestAction"]) =>
    unwrap(api.POST("/databases/{id}/migrations/{mid}/requests", {params:{path:{id,mid:m.id}},body:{action,
      target_execution_id: partial?.id, batch_size: batchSize ? Number(batchSize) : undefined}})));
  const execute = useDatabaseMutation(id, (rid:string) =>
    unwrap(api.POST("/databases/{id}/change-requests/{rid}/execute", {params:{path:{id,rid}},body:{}})));
  const cancel = useDatabaseMutation(id, (rid:string) =>
    unwrap(api.POST("/databases/{id}/change-requests/{rid}/cancel", {params:{path:{id,rid}},body:{}})));
  const busy = submit.isPending || execute.isPending || cancel.isPending;
  const error = submit.error ?? execute.error ?? cancel.error;
  return <section className="grid min-w-0 gap-4">
    {error && <ErrorState error={error}/>}
    {open ? <>
      <div className="flex flex-wrap gap-3 text-sm">
        <Link className="underline" to="/databases/$databaseId/migrations/$migrationId"
          params={{databaseId:id,migrationId:m.id}} search={{tab:"validation"}}>
          Lihat lint dan dry-run{m.latest_dry_run && ` · ${m.latest_dry_run.status}`}
        </Link>
        <Link className="underline" to="/databases/$databaseId/migrations/$migrationId"
          params={{databaseId:id,migrationId:m.id}} search={{tab:"revisions"}}>Bandingkan revisi</Link>
      </div>
      <ApprovalPanel request={open}/>
      {editable && <div className="flex flex-wrap gap-2">
        {open.status === "approved" && <Button disabled={busy} onClick={() => execute.mutate(open.id)}>Jalankan</Button>}
        <Button disabled={busy} variant="outline" onClick={() => cancel.mutate(open.id)}>Batalkan permintaan</Button>
      </div>}
    </> : editable && !active && action && <>
      {m.current.mode === "batch" && <label className="grid max-w-xs gap-1 text-sm">Ukuran batch opsional
        <input className="rounded-md border p-2" type="number" min={1} value={batchSize} onChange={(e) => setBatchSize(e.target.value)}/>
      </label>}
      <Button className="justify-self-start" disabled={busy || (action === "apply" && m.current.lint.some((f) => f.severity === "error"))}
        onClick={() => submit.mutate(action)}>{action === "apply" ? "Ajukan migrasi" : action === "forward" ? "Ajukan forward" : "Ajukan rollback"}</Button>
    </>}
    {requests.length > 0 && <>
      <h2 className="font-semibold">Riwayat permintaan</h2>
      <ul className="grid gap-2">
        {requests.map((r) => <li key={r.id} className="flex flex-wrap items-center gap-2 rounded-md border p-3 text-sm">
          <StatusBadge kind="request" status={r.status}/><span>{r.action} · Revisi {r.migration_revision}</span>
          {r.approvals.filter((a) => a.decision === "rejected" && a.comment).map((a) => <p key={a.group.id} className="w-full break-words">{a.comment}</p>)}
          {r.execution_id && <Link className="underline" to="/databases/$databaseId/executions/$executionId" params={{databaseId:id,executionId:r.execution_id}}>Lihat eksekusi</Link>}
        </li>)}
      </ul>
    </>}
  </section>;
}
