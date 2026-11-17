import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { api, unwrap, type Schemas } from "@/api/client";
import { useDatabaseEvents } from "@/api/events";
import { databaseQuery } from "@/api/queries/databases";
import { migrationListQuery, type CreateMigrationBody } from "@/api/queries/migrations";
import { PageHeader } from "@/components/PageHeader";
import { StatusBadge, statusLabel } from "@/components/StatusBadge";
import { MigrationForm } from "@/features/migrations/MigrationForm";
import { MigrationNav, PageButtons, useDatabaseMutation } from "@/features/migrations/shared";

const statuses = ["pending", "applied", "rolled_back", "superseded", "partial"] as const;

export const Route = createFileRoute("/_app/databases/$databaseId/migrations/")({
  validateSearch: (search): {status?: Schemas["MigrationStatus"]; offset?: number; create?: boolean} => ({
    status: statuses.find((value) => value === search.status),
    offset: Number.isInteger(search.offset) && Number(search.offset) > 0 ? Number(search.offset) : undefined,
    create: search.create === true ? true : undefined,
  }),
  loaderDeps: ({search}) => search,
  loader: ({context, params, deps}) => Promise.all([
    context.queryClient.ensureQueryData(databaseQuery(params.databaseId)),
    context.queryClient.ensureQueryData(migrationListQuery(params.databaseId, deps.status, deps.offset)),
  ]),
  component: MigrationList,
});

function MigrationList() {
  const {databaseId} = Route.useParams();
  const search = Route.useSearch();
  const navigate = Route.useNavigate();
  const {data: db} = useSuspenseQuery(databaseQuery(databaseId));
  const {data} = useSuspenseQuery(migrationListQuery(databaseId, search.status, search.offset));
  const create = useDatabaseMutation(databaseId, (body: CreateMigrationBody) =>
    unwrap(api.POST("/databases/{id}/migrations", {params: {path: {id: databaseId}}, body})));
  useDatabaseEvents(databaseId);
  const editable = db.effective_access === "edit";

  return <>
    <MigrationNav databaseId={databaseId}/>
    <PageHeader title={search.create && editable ? "Buat migrasi" : "Migrasi"} description={db.name} actions={editable &&
      <Link className="rounded-md border px-3 py-2 text-sm" to="." search={{...search, create: search.create ? undefined : true}}>
        {search.create ? "Kembali ke daftar" : "Buat migrasi"}
      </Link>}/>
    {search.create && editable ? <MigrationForm database={db} pending={create.isPending} error={create.error} onSave={(body) => {
      create.mutate(body, {onSuccess: ({data}) => void navigate({to: "/databases/$databaseId/migrations/$migrationId",
        params:{databaseId, migrationId: data.id}, search: {}, ignoreBlocker: true})});
    }}/> : <div className="grid gap-4">
      <label className="grid max-w-xs gap-1 text-sm">Status migrasi
        <select className="rounded-md border bg-background p-2" value={search.status ?? ""}
          onChange={(e) => void navigate({search: {status: statuses.find((value) => value === e.target.value)}})}>
          <option value="">Semua status</option>
          {statuses.map((status) => <option key={status} value={status}>{statusLabel({kind:"migration", status})}</option>)}
        </select>
      </label>
      {data.data.length === 0 && <p>Belum ada migrasi.</p>}
      <ul className="grid gap-3">
        {data.data.map((m) => <li key={m.id} className="min-w-0 rounded-lg border p-4">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <Link className="break-words font-medium underline-offset-4 hover:underline" to="/databases/$databaseId/migrations/$migrationId"
              params={{databaseId, migrationId:m.id}} search={{}}>{m.title}</Link>
            <StatusBadge kind="migration" status={m.status}/>
          </div>
          <p className="mt-1 text-sm text-muted-foreground">Revisi {m.current.revision}{m.version !== null ? ` · Versi ${m.version}` : ""}</p>
        </li>)}
      </ul>
      {(data.page.has_more || data.page.offset > 0) && <PageButtons page={data.page} onOffset={(offset) => void navigate({search: {...search, offset}})}/>}
    </div>}
  </>;
}
