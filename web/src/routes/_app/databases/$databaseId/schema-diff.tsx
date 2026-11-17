import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute } from "@tanstack/react-router";
import { useState } from "react";
import type { CreateMigrationBody } from "@/api/queries/migrations";
import { api, unwrap, type Schemas } from "@/api/client";
import { databaseQuery, databasesQuery, schemaQuery } from "@/api/queries/databases";
import { ErrorState } from "@/components/ErrorState";
import { PageHeader } from "@/components/PageHeader";
import { Button } from "@/components/ui/button";
import { MigrationForm } from "@/features/migrations/MigrationForm";
import { MigrationNav, useDatabaseMutation } from "@/features/migrations/shared";

export const Route = createFileRoute("/_app/databases/$databaseId/schema-diff")({
  loader: async ({context,params:{databaseId}}) => {
    const db = await context.queryClient.ensureQueryData(databaseQuery(databaseId));
    if (db.effective_access === "edit" && db.capabilities.schema_diff) {
      await Promise.all([context.queryClient.ensureQueryData(databasesQuery), context.queryClient.ensureQueryData(schemaQuery(databaseId))]);
    }
  },
  component: SchemaDiffPage,
});

function SchemaDiffPage() {
  const {databaseId}=Route.useParams();
  const {data:db}=useSuspenseQuery(databaseQuery(databaseId));
  return <>
    <MigrationNav databaseId={databaseId}/>
    <PageHeader title="Bandingkan schema" description={db.name}/>
    {db.effective_access === "edit" && db.capabilities.schema_diff
      ? <Comparison db={db}/> : <p>Perbandingan schema memerlukan akses edit dan target yang mendukung schema diff.</p>}
  </>;
}

function Comparison({db}: {db:Schemas["DatabaseSummary"]}) {
  const id=db.id;
  const navigate=Route.useNavigate();
  const {data:dbs}=useSuspenseQuery(databasesQuery);
  const {data:catalog}=useSuspenseQuery(schemaQuery(id));
  const [desired,setDesired]=useState("");
  const [chosenSchema,setSchema]=useState("");
  const schema=chosenSchema || catalog.schemas[0]?.name || "";
  const compare=useDatabaseMutation(id,async (input:{desired:string;schema:string})=>{
    const {data:result}=await unwrap(api.POST("/databases/{id}/schema-diff",{params:{path:{id}},
      body:{desired_database_id:input.desired,schema:input.schema,include_down:true}}));
    return {input,result,down:result.candidate_down_sql ?? ""};
  });
  const save=useDatabaseMutation(id,(body:CreateMigrationBody) =>
    unwrap(api.POST("/databases/{id}/migrations",{params:{path:{id}},body})));
  const result=compare.data?.input.desired===desired && compare.data.input.schema===schema ? compare.data : undefined;
  return <div className="grid min-w-0 gap-4">
    <label className="grid gap-1 text-sm">Database pembanding
      <select className="w-full rounded-md border bg-background p-2" value={desired} disabled={compare.isPending}
        onChange={(e)=>{setDesired(e.target.value);compare.reset();}}>
        <option value="">Pilih database</option>
        {dbs.filter((d)=>d.id!==id && d.driver===db.driver).map((d)=><option key={d.id} value={d.id}>{d.name}</option>)}
      </select>
    </label>
    <label className="grid gap-1 text-sm">Schema
      <select className="rounded-md border bg-background p-2" value={schema} disabled={compare.isPending}
        onChange={(e)=>{setSchema(e.target.value);compare.reset();}}>
        {catalog.schemas.map((s)=><option key={s.name}>{s.name}</option>)}
      </select>
    </label>
    <Button className="justify-self-start" disabled={!desired || !schema || compare.isPending}
      onClick={()=>compare.mutate({desired,schema})}>Bandingkan</Button>
    {compare.error && <ErrorState error={compare.error}/>}
    {result && <section className="grid min-w-0 gap-3">
      <h2 className="text-lg font-semibold">Perubahan schema</h2>
      {result.result.changes.length===0 && <p>Schema sama.</p>}
      <ul className="grid gap-1">
        {result.result.changes.map((c,i)=><li className="break-words" key={i}>{c.kind} · {c.object}
          {c.destructive && <span className="ml-2 text-destructive">Destruktif</span>}</li>)}
      </ul>
      {result.result.warnings.map((warning,i)=><p key={i} className="break-words text-sm">{warning}</p>)}
      {result.result.executable && result.result.candidate_sql && <>
        {!result.down && <p>Isi SQL down atau tandai migrasi sebagai irreversible sebelum menyimpan.</p>}
        <MigrationForm key={result.result.source_fingerprint+result.result.desired_fingerprint} database={db}
          up={result.result.candidate_sql} down={result.down} error={save.error} pending={save.isPending} onSave={(body)=>save.mutate({
            ...body,source_fingerprint:result.result.source_fingerprint,
          },{onSuccess:({data})=>void navigate({to:"/databases/$databaseId/migrations/$migrationId",
            params:{databaseId:id,migrationId:data.id},search:{},ignoreBlocker:true})})}/>
      </>}
    </section>}
  </div>;
}
