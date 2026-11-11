import { useMutation, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, redirect, useMatch } from "@tanstack/react-router";
import { Plus, UsersRound } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { ApiError } from "@/api/client";
import { createGroup, deleteGroup, type Group, groupsQuery, renameGroup } from "@/api/queries/groups";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { DataTable } from "@/components/DataTable";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { GroupNameForm } from "@/features/access/GroupNameForm";

export const Route = createFileRoute("/_app/admin/groups")({
  beforeLoad: ({ context }) => {
    if (!context.me.is_platform_admin) throw redirect({ to: "/databases" }); // the API still enforces it
  },
  loader: ({ context }) => context.queryClient.ensureQueryData(groupsQuery),
  component: GroupsLayout,
});

/** The list, or the detail page of one group when the URL names it. */
function GroupsLayout() {
  const detail = useMatch({ from: "/_app/admin/groups/$groupId", shouldThrow: false });
  return detail ? <Outlet /> : <GroupsPage />;
}

const message = (err: unknown) => (err instanceof ApiError ? err.message : "Permintaan gagal.");

function GroupsPage() {
  const queryClient = useQueryClient();
  const { data } = useSuspenseQuery(groupsQuery);
  const [editing, setEditing] = useState<Group | "new" | null>(null);
  const [deleting, setDeleting] = useState<Group | null>(null);
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["groups"] });

  const remove = useMutation({
    mutationFn: (g: Group) => deleteGroup(g.id),
    onSuccess: (_, g) => {
      toast.success(`Grup ${g.name} dihapus.`);
      setDeleting(null);
      void refresh();
    },
    onError: (err) => toast.error(message(err)),
  });

  async function save(name: string) {
    if (editing === "new") {
      await createGroup(name);
      toast.success(`Grup ${name} dibuat. Tambahkan anggota dan leader sebelum memberi hak akses.`);
    } else if (editing) {
      await renameGroup(editing.id, name);
      toast.success(`Grup diganti nama menjadi ${name}.`);
    }
    setEditing(null);
    await refresh();
  }

  return (
    <>
      <PageHeader
        title="Grup"
        description="Hak akses database diberikan ke grup. Setiap grup yang punya hak akses wajib punya leader, yang menyetujui perubahan pada database tersebut."
        actions={
          <Button type="button" className="h-10" onClick={() => setEditing("new")}>
            <Plus aria-hidden />
            Tambah grup
          </Button>
        }
      />
      {data.length === 0 ? (
        <EmptyState icon={UsersRound} title="Belum ada grup" description="Buat grup, tambahkan anggota, tetapkan leader, lalu beri hak akses database." />
      ) : (
        <DataTable
          caption="Daftar grup"
          rows={data}
          rowKey={(g) => g.id}
          columns={[
            {
              id: "name",
              header: "Nama",
              cell: (g) => (
                <Link to="/admin/groups/$groupId" params={{ groupId: g.id }} className="font-medium break-all underline-offset-4 hover:underline">
                  {g.name}
                </Link>
              ),
            },
            {
              id: "leader",
              header: "Leader",
              cell: (g) => (g.leader ? g.leader.username : <span className="text-muted-foreground">Belum ditetapkan</span>),
            },
            {
              id: "actions",
              header: <span className="sr-only">Aksi</span>,
              className: "whitespace-nowrap",
              cell: (g) => (
                <div className="flex gap-2">
                  <Button type="button" variant="outline" size="sm" className="h-10" onClick={() => setEditing(g)}>
                    Ganti nama
                  </Button>
                  <Button type="button" variant="ghost" size="sm" className="h-10 text-destructive" onClick={() => setDeleting(g)}>
                    Hapus
                  </Button>
                </div>
              ),
            },
          ]}
        />
      )}
      <Dialog open={editing !== null} onOpenChange={(open) => !open && setEditing(null)}>
        <DialogContent className="max-h-[90svh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{editing === "new" ? "Tambah grup" : "Ganti nama grup"}</DialogTitle>
            <DialogDescription>Nama tampil di daftar persetujuan dan riwayat audit.</DialogDescription>
          </DialogHeader>
          {editing !== null && (
            <GroupNameForm key={editing === "new" ? "new" : editing.id} current={editing === "new" ? undefined : editing.name} onSubmit={save} onCancel={() => setEditing(null)} />
          )}
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={deleting !== null}
        onOpenChange={(open) => !open && setDeleting(null)}
        title={`Hapus grup ${deleting?.name ?? ""}?`}
        description="Keanggotaan dan hak akses grup ini ikut dihapus, dan semua anggotanya keluar dari sesi."
        confirmLabel="Hapus"
        destructive
        pending={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting)}
      />
    </>
  );
}
