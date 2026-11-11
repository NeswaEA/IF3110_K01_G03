import { useMutation, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, redirect } from "@tanstack/react-router";
import { UserPlus, Users } from "lucide-react";
import { useState } from "react";
import { toast } from "sonner";
import { ApiError } from "@/api/client";
import { createUser, PAGE_SIZE, resetPassword, updateUser, usersQuery, type User } from "@/api/queries/users";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { DataTable } from "@/components/DataTable";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { CreateUserForm, ResetPasswordForm } from "@/features/access/UserForms";

type Change = { user: User; body: { active?: boolean; is_platform_admin?: boolean }; title: string; description: string; label: string; destructive?: boolean };

export const Route = createFileRoute("/_app/admin/users")({
  validateSearch: (search: Record<string, unknown>): { offset?: number } => {
    const offset = Number(search.offset);
    return Number.isInteger(offset) && offset > 0 ? { offset } : {};
  },
  beforeLoad: ({ context }) => {
    if (!context.me.is_platform_admin) throw redirect({ to: "/databases" }); // the API still enforces it
  },
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(usersQuery(deps.offset)),
  component: UsersPage,
});

const message = (err: unknown) => (err instanceof ApiError ? err.message : "Permintaan gagal.");

function UsersPage() {
  const queryClient = useQueryClient();
  const { me } = Route.useRouteContext();
  const offset = Route.useSearch().offset ?? 0;
  const { data } = useSuspenseQuery(usersQuery(offset));
  const [creating, setCreating] = useState(false);
  const [resetting, setResetting] = useState<User | null>(null);
  const [change, setChange] = useState<Change | null>(null);
  const refresh = () => queryClient.invalidateQueries({ queryKey: ["users"] });

  const update = useMutation({
    mutationFn: (c: Change) => updateUser(c.user.id, c.body),
    onSuccess: (user) => {
      toast.success(`${user.username} diperbarui.`);
      setChange(null);
      void refresh();
    },
    // LAST_ADMIN and LEADER_REQUIRED explain how to fix the refusal; show them as they are.
    onError: (err) => toast.error(message(err)),
  });

  async function create(values: { username: string; password: string; is_platform_admin: boolean }) {
    const user = await createUser(values);
    toast.success(`${user.username} dibuat.`);
    setCreating(false);
    await refresh();
  }

  async function reset(password: string) {
    if (!resetting) return;
    await resetPassword(resetting.id, password);
    toast.success(`Password ${resetting.username} diganti; sesinya diakhiri.`);
    setResetting(null);
  }

  const askActive = (user: User) =>
    setChange(
      user.active
        ? { user, body: { active: false }, title: `Nonaktifkan ${user.username}?`, description: "Pengguna ini langsung keluar dari semua sesi dan tidak dapat login.", label: "Nonaktifkan", destructive: true }
        : { user, body: { active: true }, title: `Aktifkan ${user.username}?`, description: "Pengguna ini dapat login kembali dengan password yang sama.", label: "Aktifkan" },
    );
  const askAdmin = (user: User) =>
    setChange(
      user.is_platform_admin
        ? { user, body: { is_platform_admin: false }, title: `Cabut admin dari ${user.username}?`, description: "Pengguna ini kehilangan akses ke halaman administrasi dan semua sesinya diakhiri.", label: "Cabut admin", destructive: true }
        : { user, body: { is_platform_admin: true }, title: `Jadikan ${user.username} admin?`, description: "Admin platform dapat mengelola pengguna, grup, dan koneksi, tetapi tetap tidak bisa menyetujui perubahan tanpa menjadi leader.", label: "Jadikan admin" },
    );

  return (
    <>
      <PageHeader
        title="Pengguna"
        description="Akun yang dapat login. Hak akses ke database diberikan lewat grup."
        actions={
          <Button type="button" className="h-10" onClick={() => setCreating(true)}>
            <UserPlus aria-hidden />
            Tambah pengguna
          </Button>
        }
      />
      {data.data.length === 0 ? (
        <EmptyState icon={Users} title="Tidak ada pengguna di halaman ini" description="Kembali ke halaman pertama untuk melihat daftar pengguna." />
      ) : (
        <DataTable
          caption="Daftar pengguna"
          rows={data.data}
          rowKey={(u) => u.id}
          columns={[
            {
              id: "username",
              header: "Username",
              cell: (u) => (
                <span className="font-medium break-all">
                  {u.username}
                  {u.id === me.id && <span className="ml-1 font-normal text-muted-foreground">(Anda)</span>}
                </span>
              ),
            },
            {
              id: "state",
              header: "Status",
              cell: (u) => (
                <div className="flex flex-wrap gap-1">
                  <Badge variant={u.active ? "secondary" : "outline"}>{u.active ? "Aktif" : "Nonaktif"}</Badge>
                  {u.is_platform_admin && <Badge variant="outline">Admin platform</Badge>}
                </div>
              ),
            },
            { id: "created", header: "Dibuat", cell: (u) => new Date(u.created_at).toLocaleDateString("id-ID", { dateStyle: "medium" }) },
            {
              id: "actions",
              header: <span className="sr-only">Aksi</span>,
              className: "whitespace-nowrap",
              cell: (u) => (
                <div className="flex gap-2">
                  <Button type="button" variant="outline" size="sm" className="h-10" onClick={() => setResetting(u)}>
                    Ganti password
                  </Button>
                  <Button type="button" variant="outline" size="sm" className="h-10" onClick={() => askAdmin(u)}>
                    {u.is_platform_admin ? "Cabut admin" : "Jadikan admin"}
                  </Button>
                  <Button type="button" variant="ghost" size="sm" className={u.active ? "h-10 text-destructive" : "h-10"} onClick={() => askActive(u)}>
                    {u.active ? "Nonaktifkan" : "Aktifkan"}
                  </Button>
                </div>
              ),
            },
          ]}
        />
      )}
      {(offset > 0 || data.page.has_more) && (
        <nav aria-label="Halaman daftar pengguna" className="mt-4 flex justify-end gap-2">
          {offset > 0 && (
            <Button asChild variant="outline" className="h-10">
              <Link to="/admin/users" search={{ offset: Math.max(0, offset - PAGE_SIZE) || undefined }}>
                Sebelumnya
              </Link>
            </Button>
          )}
          {data.page.has_more && (
            <Button asChild variant="outline" className="h-10">
              <Link to="/admin/users" search={{ offset: offset + PAGE_SIZE }}>
                Berikutnya
              </Link>
            </Button>
          )}
        </nav>
      )}
      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent className="max-h-[90svh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Tambah pengguna</DialogTitle>
            <DialogDescription>Akun baru belum punya akses database sampai dimasukkan ke grup.</DialogDescription>
          </DialogHeader>
          {creating && <CreateUserForm onSubmit={create} onCancel={() => setCreating(false)} />}
        </DialogContent>
      </Dialog>
      <Dialog open={resetting !== null} onOpenChange={(open) => !open && setResetting(null)}>
        <DialogContent className="max-h-[90svh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>Ganti password</DialogTitle>
            <DialogDescription>Password lama tidak dapat dilihat atau dipulihkan.</DialogDescription>
          </DialogHeader>
          {resetting && <ResetPasswordForm key={resetting.id} username={resetting.username} onSubmit={reset} onCancel={() => setResetting(null)} />}
        </DialogContent>
      </Dialog>
      <ConfirmDialog
        open={change !== null}
        onOpenChange={(open) => !open && setChange(null)}
        title={change?.title ?? ""}
        description={change?.description ?? ""}
        confirmLabel={change?.label ?? ""}
        destructive={change?.destructive}
        pending={update.isPending}
        onConfirm={() => change && update.mutate(change)}
      />
    </>
  );
}
