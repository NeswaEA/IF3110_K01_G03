import { useMutation, useQueryClient, useSuspenseQuery } from "@tanstack/react-query";
import { Link } from "@tanstack/react-router";
import { useState } from "react";
import { toast } from "sonner";
import { ApiError } from "@/api/client";
import { connectionsQuery } from "@/api/queries/connections";
import {
  addMember, allUsersQuery, type Grant, type Group, groupGrantsQuery, groupMembersQuery, removeGrant, removeMember, setGrant, setLeader,
} from "@/api/queries/groups";
import type { User } from "@/api/queries/users";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { DataTable } from "@/components/DataTable";
import { EmptyState } from "@/components/EmptyState";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

const message = (err: unknown) => (err instanceof ApiError ? err.message : "Permintaan gagal.");
const LEVELS: Record<Grant["level"], string> = { view: "Lihat", edit: "Lihat dan ubah" };

/** Mutation that refreshes the group's data and reports the outcome as a toast. */
function useGroupMutation<T>(fn: (value: T) => Promise<unknown>, success: (value: T) => string, done?: () => void) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: (_, value) => {
      toast.success(success(value));
      done?.();
    },
    // LEADER_REQUIRED says how to fix the refusal; the message is shown as the API wrote it.
    onError: (err) => toast.error(message(err)),
    onSettled: () => Promise.all([queryClient.invalidateQueries({ queryKey: ["groups"] }), queryClient.invalidateQueries({ queryKey: ["users"] })]),
  });
}

export function MembersTab({ group }: { group: Group }) {
  const members = useSuspenseQuery(groupMembersQuery(group.id)).data;
  const users = useSuspenseQuery(allUsersQuery).data;
  const [chosen, setChosen] = useState("");
  const [removing, setRemoving] = useState<User | null>(null);
  const candidates = users.filter((u) => u.active && !members.some((m) => m.id === u.id));
  const add = useGroupMutation((userId: string) => addMember(group.id, userId), (id) => `${users.find((u) => u.id === id)?.username} ditambahkan.`, () => setChosen(""));
  const remove = useGroupMutation((u: User) => removeMember(group.id, u.id), (u) => `${u.username} dikeluarkan dari grup.`, () => setRemoving(null));

  return (
    <div className="grid gap-4">
      <form
        className="flex flex-col gap-2 sm:flex-row sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          if (chosen) add.mutate(chosen);
        }}
      >
        <div className="grid flex-1 gap-1.5">
          <Label htmlFor="member-user">Tambah anggota</Label>
          <Select value={chosen} onValueChange={setChosen} disabled={candidates.length === 0}>
            <SelectTrigger id="member-user" className="h-10 w-full">
              <SelectValue placeholder={candidates.length === 0 ? "Semua pengguna aktif sudah menjadi anggota" : "Pilih pengguna"} />
            </SelectTrigger>
            <SelectContent>
              {candidates.map((u) => (
                <SelectItem key={u.id} value={u.id}>
                  {u.username}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <Button type="submit" className="h-10" disabled={!chosen || add.isPending}>
          Tambah
        </Button>
      </form>
      {members.length === 0 ? (
        <EmptyState title="Grup ini belum punya anggota" description="Tambahkan anggota, lalu pilih salah satunya sebagai leader." />
      ) : (
        <DataTable
          caption={`Anggota ${group.name}`}
          rows={members}
          rowKey={(u) => u.id}
          columns={[
            { id: "username", header: "Username", cell: (u) => <span className="font-medium break-all">{u.username}</span> },
            {
              id: "role",
              header: "Peran",
              cell: (u) => (
                <div className="flex flex-wrap gap-1">
                  {u.id === group.leader?.id && <Badge variant="secondary">Leader</Badge>}
                  {!u.active && <Badge variant="outline">Nonaktif</Badge>}
                </div>
              ),
            },
            {
              id: "actions",
              header: <span className="sr-only">Aksi</span>,
              className: "whitespace-nowrap",
              cell: (u) => (
                <Button type="button" variant="ghost" size="sm" className="h-10 text-destructive" onClick={() => setRemoving(u)}>
                  Keluarkan
                </Button>
              ),
            },
          ]}
        />
      )}
      <ConfirmDialog
        open={removing !== null}
        onOpenChange={(open) => !open && setRemoving(null)}
        title={`Keluarkan ${removing?.username ?? ""}?`}
        description={
          removing?.id === group.leader?.id
            ? "Pengguna ini leader grup. Tetapkan leader lain di tab Leader sebelum mengeluarkannya."
            : "Pengguna ini kehilangan hak akses dari grup ini dan keluar dari semua sesinya."
        }
        confirmLabel="Keluarkan"
        destructive
        pending={remove.isPending}
        onConfirm={() => removing && remove.mutate(removing)}
      />
    </div>
  );
}

export function LeaderTab({ group }: { group: Group }) {
  const members = useSuspenseQuery(groupMembersQuery(group.id)).data.filter((m) => m.active);
  const [chosen, setChosen] = useState(group.leader?.id ?? "");
  const save = useGroupMutation((userId: string) => setLeader(group.id, userId), (id) => `${members.find((m) => m.id === id)?.username} sekarang leader ${group.name}.`);

  return (
    <form
      className="grid max-w-xl gap-4"
      onSubmit={(e) => {
        e.preventDefault();
        if (chosen) save.mutate(chosen);
      }}
    >
      <p className="text-sm text-muted-foreground">
        Leader menyetujui atau menolak setiap perubahan pada database yang dapat diakses grup ini. Satu grup punya tepat satu leader, dipilih dari anggota aktif.
      </p>
      <div className="grid gap-1.5">
        <Label htmlFor="leader-user">Leader</Label>
        <Select value={chosen} onValueChange={setChosen} disabled={members.length === 0}>
          <SelectTrigger id="leader-user" className="h-10 w-full">
            <SelectValue placeholder={members.length === 0 ? "Tambahkan anggota aktif lebih dulu" : "Pilih anggota"} />
          </SelectTrigger>
          <SelectContent>
            {members.map((m) => (
              <SelectItem key={m.id} value={m.id}>
                {m.username}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
      <div>
        <Button type="submit" className="h-10" disabled={!chosen || chosen === group.leader?.id || save.isPending}>
          Simpan leader
        </Button>
      </div>
    </form>
  );
}

export function GrantsTab({ group }: { group: Group }) {
  const grants = useSuspenseQuery(groupGrantsQuery(group.id)).data;
  const databases = useSuspenseQuery(connectionsQuery).data;
  const [database, setDatabase] = useState("");
  const [level, setLevel] = useState<Grant["level"]>("view");
  const [revoking, setRevoking] = useState<Grant | null>(null);
  const nameOf = (id: string) => databases.find((d) => d.id === id)?.name ?? id;
  const candidates = databases.filter((d) => d.active && !grants.some((g) => g.database_id === d.id));
  const change = useGroupMutation((g: Grant) => setGrant(group.id, g.database_id, g.level), (g) => `${nameOf(g.database_id)}: ${LEVELS[g.level].toLowerCase()}.`, () => setDatabase(""));
  const revoke = useGroupMutation((g: Grant) => removeGrant(group.id, g.database_id), (g) => `Hak akses ke ${nameOf(g.database_id)} dicabut.`, () => setRevoking(null));

  return (
    <div className="grid gap-4">
      {!group.leader && (
        <Alert>
          <AlertTitle>Tetapkan leader sebelum memberi hak akses</AlertTitle>
          <AlertDescription>
            Perubahan pada database harus disetujui leader setiap grup yang punya akses.{" "}
            <Link to="/admin/groups/$groupId" params={{ groupId: group.id }} search={{ tab: "leader" }} className="underline underline-offset-4">
              Pilih leader
            </Link>
          </AlertDescription>
        </Alert>
      )}
      <form
        className="flex flex-col gap-2 sm:flex-row sm:items-end"
        onSubmit={(e) => {
          e.preventDefault();
          if (database) change.mutate({ database_id: database, level });
        }}
      >
        <div className="grid flex-1 gap-1.5">
          <Label htmlFor="grant-database">Database</Label>
          <Select value={database} onValueChange={setDatabase} disabled={candidates.length === 0}>
            <SelectTrigger id="grant-database" className="h-10 w-full">
              <SelectValue placeholder={candidates.length === 0 ? "Semua database aktif sudah diberikan" : "Pilih database"} />
            </SelectTrigger>
            <SelectContent>
              {candidates.map((d) => (
                <SelectItem key={d.id} value={d.id}>
                  {d.name}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
        <div className="grid gap-1.5 sm:w-48">
          <Label htmlFor="grant-level">Tingkat</Label>
          <LevelSelect id="grant-level" value={level} onChange={setLevel} />
        </div>
        <Button type="submit" className="h-10" disabled={!database || change.isPending}>
          Beri akses
        </Button>
      </form>
      {grants.length === 0 ? (
        <EmptyState title="Grup ini belum punya hak akses" description="Pilih database dan tingkat akses di atas." />
      ) : (
        <DataTable
          caption={`Hak akses ${group.name}`}
          rows={grants}
          rowKey={(g) => g.database_id}
          columns={[
            { id: "database", header: "Database", cell: (g) => <span className="font-medium break-all">{nameOf(g.database_id)}</span> },
            {
              id: "level",
              header: "Tingkat",
              cell: (g) => (
                <LevelSelect
                  label={`Tingkat akses ${nameOf(g.database_id)}`}
                  value={g.level}
                  onChange={(next) => next !== g.level && change.mutate({ database_id: g.database_id, level: next })}
                />
              ),
            },
            {
              id: "actions",
              header: <span className="sr-only">Aksi</span>,
              className: "whitespace-nowrap",
              cell: (g) => (
                <Button type="button" variant="ghost" size="sm" className="h-10 text-destructive" onClick={() => setRevoking(g)}>
                  Cabut
                </Button>
              ),
            },
          ]}
        />
      )}
      <ConfirmDialog
        open={revoking !== null}
        onOpenChange={(open) => !open && setRevoking(null)}
        title={`Cabut akses ke ${revoking ? nameOf(revoking.database_id) : ""}?`}
        description="Anggota grup ini kehilangan akses ke database tersebut, kecuali lewat grup lain, dan keluar dari sesinya."
        confirmLabel="Cabut"
        destructive
        pending={revoke.isPending}
        onConfirm={() => revoking && revoke.mutate(revoking)}
      />
    </div>
  );
}

function LevelSelect({ id, label, value, onChange }: { id?: string; label?: string; value: Grant["level"]; onChange: (level: Grant["level"]) => void }) {
  return (
    <Select value={value} onValueChange={(v) => onChange(v as Grant["level"])}>
      <SelectTrigger id={id} aria-label={label} className="h-10 w-full min-w-40">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value="view">{LEVELS.view}</SelectItem>
        <SelectItem value="edit">{LEVELS.edit}</SelectItem>
      </SelectContent>
    </Select>
  );
}
