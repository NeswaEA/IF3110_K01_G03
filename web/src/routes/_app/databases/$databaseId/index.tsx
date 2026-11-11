import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { databaseQuery } from "@/api/queries/databases";
import { PageHeader } from "@/components/PageHeader";
import { Label } from "@/components/ui/label";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Textarea } from "@/components/ui/textarea";
import { ExplainPanel } from "@/features/explain/ExplainPanel";
import { SchemaTree } from "@/features/viewer/SchemaTree";
import { TableDataView } from "@/features/viewer/TableDataView";

type Tab = "schema" | "data" | "explain";
export type ViewerSearch = { tab?: Tab; schema?: string; table?: string; offset?: number };

export const Route = createFileRoute("/_app/databases/$databaseId/")({
  // The URL keeps the open tab, table and page, so a view can be shared and survives a refresh.
  validateSearch: (s: Record<string, unknown>): ViewerSearch => {
    const offset = Number(s.offset);
    return {
      tab: s.tab === "data" || s.tab === "explain" ? s.tab : undefined,
      schema: typeof s.schema === "string" && s.schema !== "" ? s.schema : undefined,
      table: typeof s.table === "string" && s.table !== "" ? s.table : undefined,
      offset: Number.isInteger(offset) && offset > 0 ? offset : undefined,
    };
  },
  loader: ({ context, params }) => context.queryClient.ensureQueryData(databaseQuery(params.databaseId)),
  component: ViewerPage,
});

function ViewerPage() {
  const { databaseId } = Route.useParams();
  const search = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const { data: db } = useSuspenseQuery(databaseQuery(databaseId));
  const [sql, setSql] = useState("");
  const tab = search.tab ?? "schema";
  const canExplain = db.effective_access === "edit" && db.capabilities.explain;
  const go = (next: ViewerSearch) => void navigate({ search: next });

  return (
    <>
      <PageHeader title={db.name} description="Schema dan isi tabel dibaca langsung dari database target." />
      <Tabs value={tab} onValueChange={(v) => go({ ...search, tab: v === "schema" ? undefined : (v as Tab) })}>
        <TabsList className="h-10">
          <TabsTrigger value="schema">Schema</TabsTrigger>
          <TabsTrigger value="data">Data</TabsTrigger>
          {canExplain && <TabsTrigger value="explain">Rencana kueri</TabsTrigger>}
        </TabsList>
        <TabsContent value="schema" className="mt-4">
          <SchemaTree databaseId={databaseId} onOpenTable={(schema, table) => go({ tab: "data", schema, table })} />
        </TabsContent>
        <TabsContent value="data" className="mt-4">
          <TableDataView
            databaseId={databaseId}
            schema={search.schema}
            table={search.table}
            offset={search.offset ?? 0}
            onChange={(next) => go({ tab: "data", ...next })}
          />
        </TabsContent>
        {canExplain && (
          <TabsContent value="explain" className="mt-4 grid gap-4">
            <div className="grid gap-2">
              <Label htmlFor="explain-sql">Satu statement SQL</Label>
              <Textarea id="explain-sql" className="min-h-32 font-mono" value={sql} onChange={(e) => setSql(e.target.value)} spellCheck={false} />
            </div>
            <ExplainPanel databaseId={databaseId} sql={sql} canExplain={canExplain} />
          </TabsContent>
        )}
      </Tabs>
    </>
  );
}
