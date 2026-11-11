import { useMutation, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, redirect } from "@tanstack/react-router";
import { Plus, Server } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { ApiError, type Schemas } from "@/api/client";
import { connectionsQuery, createConnection, deactivateConnection, testConnection, updateConnection } from "@/api/queries/connections";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { DataTable } from "@/components/DataTable";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { ConnectionForm, type ConnectionValues } from "@/features/connections/ConnectionForm";

type Connection = Schemas["AdminConnection"];

export const Route = createFileRoute("/_app/admin/connections")({
  beforeLoad: ({ context }) => {
    if (!context.me.is_platform_admin) throw redirect({ to: "/databases" }); // the API still enforces it
  },
  loader: ({ context }) => context.queryClient.ensureQueryData(connectionsQuery),
  component: ConnectionsPage,
});

const message = (err: unknown) => (err instanceof ApiError ? err.message : "Permintaan gagal.");

function ConnectionsPage() {
  const queryClient = useQueryClient();
  const { data } = useSuspenseQuery(connectionsQuery);
  const [editing, setEditing] = useState<Connection | "new" | null>(null);
  const [deactivating, setDeactivating] = useState<Connection | null>(null);
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["connections"] }).then(() => queryClient.invalidateQueries({ queryKey: ["databases"] }));

  const test = useMutation({
    mutationFn: (c: Connection) => testConnection(c.id),
    onSuccess: (res, c) => {
      toast.success(`${c.name}: terhubung dalam ${res.latency_ms} ms.`);
      void refresh();
    },
    onError: (err, c) => {
      toast.error(`${c.name}: ${message(err)}`);
      void refresh();
    },
  });
  const deactivate = useMutation({
    mutationFn: (c: Connection) => deactivateConnection(c.id),
    onSuccess: (_, c) => {
      toast.success(`${c.name} dinonaktifkan.`);
      setDeactivating(null);
      void refresh();
    },
    onError: (err) => toast.error(message(err)),
  });

  async function save(values: ConnectionValues) {
    if (editing === "new") {
      await createConnection({ name: values.name, driver: values.driver, config: values.config as Schemas["ConnectionCreate"]["config"] });
      toast.success(`${values.name} terdaftar.`);
    } else if (editing) {
      await updateConnection(editing.id, { revision: editing.revision, name: values.name, config: values.config });
      toast.success(`${values.name} diperbarui.`);
    }
    setEditing(null);
    await refresh();
  }

  return (
    <>
      <PageHeader
        title="Koneksi database"
        description="Database target yang dapat dipakai. Password tidak pernah ditampilkan kembali."
        actions={
          <Button type="button" className="h-10" onClick={() => setEditing("new")}>
            <Plus aria-hidden />
            Tambah koneksi
          </Button>
        }
      />
      {data.length === 0 ? (
        <EmptyState icon={Server} title="Belum ada koneksi" description="Tambahkan database PostgreSQL atau SQLite pertama." />
      ) : (
        <DataTable
          caption="Daftar koneksi database"
          rows={data}
          rowKey={(c) => c.id}
          columns={[
            { id: "name", header: "Nama", cell: (c) => <span className="font-medium">{c.name}</span> },
            { id: "driver", header: "Engine", cell: (c) => (c.driver === "postgres" ? "PostgreSQL" : "SQLite") },
            {
              id: "state",
              header: "Status",
              cell: (c) => (
                <div className="flex flex-wrap gap-1">
                  <Badge variant={c.active ? "secondary" : "outline"}>{c.active ? "Aktif" : "Nonaktif"}</Badge>
                  <Badge variant="outline">{c.migration_ready ? "Siap migrasi" : "Belum siap migrasi"}</Badge>
                </div>
              ),
            },
            {
              id: "check",
              header: "Pemeriksaan terakhir",
              cell: (c) =>
                c.last_checked_at === null ? (
                  <span className="text-muted-foreground">Belum pernah</span>
                ) : (
                  <span>
                    {new Date(c.last_checked_at).toLocaleString("id-ID")}
                    {c.last_error_code && <span className="block text-destructive">{c.last_error_code}</span>}
                  </span>
                ),
            },
            {
              id: "actions",
              header: <span className="sr-only">Aksi</span>,
              className: "whitespace-nowrap",
              cell: (c) =>
                c.active && (
                  <div className="flex gap-2">
                    <Button type="button" variant="outline" size="sm" className="h-10" onClick={() => setEditing(c)}>
                      Ubah
                    </Button>
                    <Button type="button" variant="outline" size="sm" className="h-10" disabled={test.isPending} onClick={() => test.mutate(c)}>
                      Uji
                    </Button>
                    <Button type="button" variant="ghost" size="sm" className="h-10 text-destructive" onClick={() => setDeactivating(c)}>
                      Nonaktifkan
                    </Button>
                  </div>
                ),
            },
          ]}
        />
      )}
      <Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="max-h-[90svh] overflow-y-auto sm:max-w-xl">
          <DialogHeader>
            <DialogTitle>{editing === "new" ? "Tambah koneksi" : "Ubah koneksi"}</DialogTitle>
            <DialogDescription>Koneksi diuji dan disiapkan sebelum disimpan.</DialogDescription>
          </DialogHeader>
          {editing !== null && (
            <ConnectionForm key={editing === "new" ? "new" : editing.id} current={editing === "new" ? undefined : editing} onSubmit={save} onCancel={() => setEditing(null)} />
          )}
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={deactivating !== null}
        onOpenChange={(open) => !open && setDeactivating(null)}
        title="Nonaktifkan koneksi?"
        description={`${deactivating?.name ?? ""} tidak lagi dapat dipakai dan seluruh grant ke database ini dicabut.`}
        confirmLabel="Nonaktifkan"
        destructive
        pending={deactivate.isPending}
        onConfirm={() => deactivating && deactivate.mutate(deactivating)}
      />
    </>
  );
}
