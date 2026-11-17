import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Link, useBlocker } from "@tanstack/react-router";
import { ConfirmDialog } from "@/components/ConfirmDialog";
import { Button } from "@/components/ui/button";
import type { Schemas } from "@/api/client";

export function MigrationNav({ databaseId }: {databaseId: string}) {
  return <nav aria-label="Menu database" className="mb-5 flex flex-wrap gap-2 text-sm">
    <Link className="rounded-md border px-3 py-2" to="/databases/$databaseId" params={{ databaseId }}>Database</Link>
    <Link className="rounded-md border px-3 py-2" to="/databases/$databaseId/migrations" params={{ databaseId }}>Migrasi</Link>
    <Link className="rounded-md border px-3 py-2" to="/databases/$databaseId/history" params={{ databaseId }}>Riwayat</Link>
    <Link className="rounded-md border px-3 py-2" to="/databases/$databaseId/schema-diff" params={{ databaseId }}>Bandingkan schema</Link>
  </nav>;
}

export function UnsavedDialog({ dirty }: {dirty: boolean}) {
  const blocker = useBlocker({
    disabled: !dirty, shouldBlockFn: () => dirty, enableBeforeUnload: dirty, withResolver: true,
  });
  return <ConfirmDialog open={blocker.status === "blocked"}
    onOpenChange={(open) => { if (!open) blocker.reset?.(); }}
    title="Tinggalkan perubahan?"
    description="Perubahan yang belum disimpan akan hilang."
    confirmLabel="Tinggalkan" destructive onConfirm={() => blocker.proceed?.()} />;
}

export function useDatabaseMutation<T, Input>(id: string, mutationFn: (input: Input) => Promise<T>) {
  const client = useQueryClient();
  return useMutation({
    mutationFn,
    onSuccess: () => client.invalidateQueries({ predicate: (q) => q.queryKey[1] === id || q.queryKey[0] === "reviews" }),
  });
}

export function PageButtons({ page, onOffset }: {page: Schemas["Page"]; onOffset: (offset: number) => void}) {
  return <div className="flex flex-wrap gap-2">
    <Button variant="outline" disabled={page.offset === 0} onClick={() => onOffset(Math.max(0, page.offset - page.limit))}>Sebelumnya</Button>
    <Button variant="outline" disabled={!page.has_more} onClick={() => onOffset(page.offset + page.limit)}>Berikutnya</Button>
  </div>;
}
