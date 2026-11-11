import { useQuery } from "@tanstack/react-query";
import { ChevronLeft, ChevronRight, TableProperties } from "lucide-react";
import type { Schemas } from "@/api/client";
import { PAGE_SIZE, schemaQuery, tableDataQuery } from "@/api/queries/databases";
import { DataTable } from "@/components/DataTable";
import { EmptyState } from "@/components/EmptyState";
import { ErrorState } from "@/components/ErrorState";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";

type Encoding = Schemas["Encoding"];

/** One value as text: NULL stays distinct from "", bigints stay strings, binary stays base64. */
function Cell({ value, encoding }: { value: unknown; encoding: Encoding }) {
  if (value === null) return <em className="text-muted-foreground">NULL</em>;
  if (encoding === "base64") {
    return (
      <span className="font-mono text-xs" title="Data biner (base64)">
        {String(value)}
      </span>
    );
  }
  if (encoding === "json") return <code className="text-xs">{JSON.stringify(value)}</code>;
  return <>{String(value)}</>;
}

export function TableDataView({ databaseId, schema, table, offset, onChange }: {
  databaseId: string;
  schema?: string;
  table?: string;
  offset: number;
  onChange: (next: { schema?: string; table?: string; offset?: number }) => void;
}) {
  const snapshot = useQuery(schemaQuery(databaseId));
  const data = useQuery({ ...tableDataQuery(databaseId, schema ?? "", table ?? "", offset), enabled: !!schema && !!table });
  const tables = snapshot.data?.schemas.flatMap((s) => s.tables.map((t) => `${s.name}.${t.name}`)) ?? [];
  const selected = schema && table ? `${schema}.${table}` : "";

  return (
    <div className="grid gap-4">
      <div className="grid max-w-md gap-1.5">
        <Label htmlFor="data-table">Tabel</Label>
        <Select
          value={selected}
          onValueChange={(v) => {
            const dot = v.indexOf(".");
            onChange({ schema: v.slice(0, dot), table: v.slice(dot + 1), offset: undefined });
          }}
          disabled={tables.length === 0}
        >
          <SelectTrigger id="data-table" className="h-10 w-full">
            <SelectValue placeholder={snapshot.isPending ? "Memuat tabel…" : "Pilih tabel"} />
          </SelectTrigger>
          <SelectContent>
            {selected && !tables.includes(selected) && <SelectItem value={selected}>{selected}</SelectItem>}
            {tables.map((t) => (
              <SelectItem key={t} value={t}>
                {t}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      {snapshot.isError && <ErrorState error={snapshot.error} onRetry={() => void snapshot.refetch()} />}
      {!selected ? (
        <EmptyState icon={TableProperties} title="Pilih tabel" description="Isi tabel ditampilkan per halaman 50 baris." />
      ) : data.isPending ? (
        <Skeleton className="h-60 w-full" aria-label="Memuat data" />
      ) : data.isError ? (
        <ErrorState error={data.error} onRetry={() => void data.refetch()} />
      ) : (
        <>
          {!data.data.data.order_stable && (
            <Alert>
              <AlertDescription>
                Tabel ini tidak memiliki primary key, sehingga urutan baris antarhalaman tidak dijamin.
              </AlertDescription>
            </Alert>
          )}
          {data.data.data.rows.length === 0 ? (
            <EmptyState title="Tidak ada baris" description={offset > 0 ? "Halaman ini kosong." : "Tabel ini kosong."} />
          ) : (
            <DataTable
              caption={`Isi tabel ${selected}`}
              rows={data.data.data.rows}
              rowKey={(_, i) => String(offset + i)}
              columns={data.data.data.columns.map((c, i) => ({
                id: `${i}-${c.name}`,
                header: (
                  <span title={c.data_type}>
                    {c.name} <span className="font-normal text-muted-foreground">{c.data_type}</span>
                  </span>
                ),
                cell: (row: unknown[]) => <Cell value={row[i]} encoding={c.encoding} />,
                className: "whitespace-nowrap",
              }))}
            />
          )}
          <nav className="flex flex-wrap items-center justify-between gap-2" aria-label="Halaman data">
            <span className="text-sm text-muted-foreground" aria-live="polite">
              {data.data.data.rows.length > 0 ? `Baris ${offset + 1}–${offset + data.data.data.rows.length}` : "Tidak ada baris"}
            </span>
            <div className="flex gap-2">
              <Button type="button" variant="outline" className="h-10" disabled={offset === 0 || data.isFetching}
                onClick={() => onChange({ schema, table, offset: Math.max(0, offset - PAGE_SIZE) || undefined })}>
                <ChevronLeft aria-hidden />
                Sebelumnya
              </Button>
              <Button type="button" variant="outline" className="h-10" disabled={!data.data.page.has_more || data.isFetching}
                onClick={() => onChange({ schema, table, offset: offset + PAGE_SIZE })}>
                Berikutnya
                <ChevronRight aria-hidden />
              </Button>
            </div>
          </nav>
        </>
      )}
    </div>
  );
}
