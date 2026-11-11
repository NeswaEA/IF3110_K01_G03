import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { Database, SearchX } from "lucide-react";
import { databasesQuery } from "@/api/queries/databases";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { Badge } from "@/components/ui/badge";
import { Card, CardDescription, CardHeader, CardTitle } from "@/components/ui/card";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

type AccessFilter = "all" | "edit" | "view";
type Search = { q?: string; access?: AccessFilter };

export const Route = createFileRoute("/_app/databases/")({
  validateSearch: (s: Record<string, unknown>): Search => ({
    q: typeof s.q === "string" && s.q !== "" ? s.q : undefined,
    access: s.access === "edit" || s.access === "view" ? s.access : undefined,
  }),
  loader: ({ context }) => context.queryClient.ensureQueryData(databasesQuery),
  component: DatabasesPage,
});

function DatabasesPage() {
  const { q = "", access = "all" } = Route.useSearch();
  const navigate = useNavigate({ from: Route.fullPath });
  const { data } = useSuspenseQuery(databasesQuery);
  const needle = q.trim().toLowerCase();
  const shown = data.filter(
    (d) => (access === "all" || d.effective_access === access) && (needle === "" || d.name.toLowerCase().includes(needle)),
  );
  const setSearch = (next: Search) => void navigate({ search: (prev) => ({ ...prev, ...next }), replace: true });

  return (
    <>
      <PageHeader title="Database" description="Database yang boleh Anda lihat atau ubah." />
      {data.length === 0 ? (
        <EmptyState icon={Database} title="Belum ada database" description="Minta admin memberi akses grup Anda ke sebuah database." />
      ) : (
        <>
          <div className="mb-4 grid gap-3 sm:grid-cols-[1fr_12rem]">
            <div className="grid gap-1.5">
              <Label htmlFor="db-search">Cari nama</Label>
              <Input id="db-search" type="search" value={q} onChange={(e) => setSearch({ q: e.target.value || undefined })} />
            </div>
            <div className="grid gap-1.5">
              <Label htmlFor="db-access">Akses</Label>
              <Select value={access} onValueChange={(v) => setSearch({ access: v === "all" ? undefined : (v as AccessFilter) })}>
                <SelectTrigger id="db-access" className="h-10 w-full">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">Semua</SelectItem>
                  <SelectItem value="edit">Edit</SelectItem>
                  <SelectItem value="view">Lihat saja</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>
          {shown.length === 0 ? (
            <EmptyState icon={SearchX} title="Tidak ada yang cocok" description="Ubah kata kunci atau filter akses." />
          ) : (
            <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3" aria-label="Daftar database">
              {shown.map((d) => (
                <li key={d.id}>
                  <Link to="/databases/$databaseId" params={{ databaseId: d.id }} className="block rounded-xl focus-visible:ring-2 focus-visible:ring-ring focus-visible:outline-none">
                    <Card className="h-full transition-colors hover:bg-muted/50">
                      <CardHeader>
                        <CardTitle className="truncate" title={d.name}>
                          {d.name}
                        </CardTitle>
                        <CardDescription className="flex flex-wrap gap-2 pt-1">
                          <Badge variant="outline">{d.driver === "postgres" ? "PostgreSQL" : "SQLite"}</Badge>
                          <Badge variant="secondary">{d.effective_access === "edit" ? "Akses edit" : "Akses lihat"}</Badge>
                        </CardDescription>
                      </CardHeader>
                    </Card>
                  </Link>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </>
  );
}
