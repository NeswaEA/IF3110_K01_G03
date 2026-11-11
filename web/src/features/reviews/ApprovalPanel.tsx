import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Clock, type LucideIcon, XCircle } from "lucide-react";
import { type FormEvent, useState } from "react";
import { toast } from "sonner";
import { ApiError, type Schemas } from "@/api/client";
import { meQuery } from "@/api/queries/auth";
import { approveRequest, type ChangeRequest, rejectRequest } from "@/api/queries/reviews";
import { StatusBadge } from "@/components/StatusBadge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";

type Decision = Schemas["RequiredApproval"]["decision"];

const decisions: Record<Decision, { label: string; icon: LucideIcon; className: string }> = {
  pending: { label: "Menunggu", icon: Clock, className: "text-muted-foreground" },
  approved: { label: "Disetujui", icon: CheckCircle2, className: "text-emerald-700" },
  rejected: { label: "Ditolak", icon: XCircle, className: "text-destructive" },
};

const actions: Record<Schemas["RequestAction"], string> = { apply: "menerapkan", rollback: "me-rollback", forward: "menerapkan ulang (forward)" };

/**
 * Who must approve a change request and what each leader decided. A leader of a pending group
 * decides here; one decision covers every pending group they lead. The API stays the authority.
 */
export function ApprovalPanel({ request }: { request: ChangeRequest }) {
  const me = useQuery(meQuery).data;
  const queryClient = useQueryClient();
  const [deciding, setDeciding] = useState<"approve" | "reject" | null>(null);
  const open = request.status === "pending_review" || request.status === "approved";
  const mine = request.approvals.filter((a) => a.decision === "pending" && a.leader?.id === me?.id);

  async function decide(comment: string) {
    await (deciding === "approve" ? approveRequest(request, comment) : rejectRequest(request, comment));
    toast.success(deciding === "approve" ? "Persetujuan dicatat." : "Penolakan dicatat.");
    setDeciding(null);
    await Promise.all(
      [["requests", request.database_id], ["migrations", request.database_id], ["reviews"]].map((queryKey) => queryClient.invalidateQueries({ queryKey })),
    );
  }

  return (
    <section aria-labelledby={`approval-${request.id}`} className="grid gap-4 rounded-lg border p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 id={`approval-${request.id}`} className="text-lg font-semibold">
          Persetujuan
        </h2>
        <StatusBadge kind="request" status={request.status} />
      </div>
      <p className="text-sm text-muted-foreground">
        {request.requested_by.username} meminta {actions[request.action]} revisi {request.migration_revision}. Leader setiap grup yang punya akses ke database ini harus menyetujui.
      </p>
      {request.approvals.length === 0 ? (
        <p className="text-sm">Belum ada grup dengan akses ke database ini, jadi belum ada yang dapat menyetujui. Minta admin memberi hak akses ke grup yang punya leader.</p>
      ) : (
        <ul className="grid gap-2">
          {request.approvals.map((a) => {
            const look = decisions[a.decision];
            return (
              <li key={a.group.id} className="grid gap-1 rounded-md border p-3 sm:grid-cols-[1fr_auto] sm:items-start">
                <div className="min-w-0">
                  <p className="font-medium break-all">{a.group.name}</p>
                  <p className="text-sm text-muted-foreground break-all">Leader: {a.leader ? a.leader.username : "belum ditetapkan"}</p>
                  {a.comment && <p className="mt-1 text-sm break-words whitespace-pre-wrap">{a.comment}</p>}
                </div>
                <p className={`flex items-center gap-1 text-sm font-medium ${look.className}`}>
                  <look.icon aria-hidden className="size-4" />
                  {look.label}
                  {a.decided_at && <span className="font-normal text-muted-foreground">· {new Date(a.decided_at).toLocaleString("id-ID")}</span>}
                </p>
              </li>
            );
          })}
        </ul>
      )}
      {open && mine.length > 0 && (
        <div className="grid gap-2">
          <p className="text-sm">
            Keputusan Anda berlaku untuk grup <span className="font-medium">{mine.map((a) => a.group.name).join(", ")}</span>.
          </p>
          <div className="flex flex-wrap gap-2">
            <Button type="button" className="h-10" onClick={() => setDeciding("approve")}>
              Setujui
            </Button>
            <Button type="button" variant="outline" className="h-10 text-destructive" onClick={() => setDeciding("reject")}>
              Tolak
            </Button>
          </div>
        </div>
      )}
      <Dialog open={deciding !== null} onOpenChange={(o) => !o && setDeciding(null)}>
        <DialogContent className="max-h-[90svh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{deciding === "reject" ? "Tolak perubahan" : "Setujui perubahan"}</DialogTitle>
            <DialogDescription>
              {request.migration_title}, revisi {request.migration_revision}. Bila SQL diubah setelah ini, keputusan Anda dibatalkan dan perlu diulang.
            </DialogDescription>
          </DialogHeader>
          {deciding && <DecisionForm reject={deciding === "reject"} onSubmit={decide} onCancel={() => setDeciding(null)} />}
        </DialogContent>
      </Dialog>
    </section>
  );
}

function DecisionForm({ reject, onSubmit, onCancel }: { reject: boolean; onSubmit: (comment: string) => Promise<void>; onCancel: () => void }) {
  const [error, setError] = useState<ApiError | null>(null);
  const [pending, setPending] = useState(false);
  const fieldError = error?.fields.comment;

  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setPending(true);
    setError(null);
    try {
      await onSubmit(String(new FormData(e.currentTarget).get("comment") ?? "").trim());
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError({ code: "INTERNAL_ERROR", message: "Keputusan gagal dikirim." }, 0));
    } finally {
      setPending(false);
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-4">
      <div className="grid gap-1.5">
        <Label htmlFor="decision-comment">{reject ? "Alasan penolakan" : "Komentar (opsional)"}</Label>
        <Textarea id="decision-comment" name="comment" rows={4} maxLength={2000} required={reject} aria-invalid={!!fieldError} aria-describedby="decision-comment-help" />
        <p id="decision-comment-help" role={fieldError ? "alert" : undefined} className={fieldError ? "text-sm text-destructive" : "text-sm text-muted-foreground"}>
          {fieldError ?? (reject ? "Wajib, maksimal 2000 karakter. Penolakan menutup permintaan ini." : "Maksimal 2000 karakter.")}
        </p>
      </div>
      {error && !fieldError && (
        <p role="alert" className="text-sm text-destructive">
          {error.message}
        </p>
      )}
      <DialogFooter>
        <Button type="button" variant="outline" className="h-10" onClick={onCancel} disabled={pending}>
          Batal
        </Button>
        <Button type="submit" variant={reject ? "destructive" : "default"} className="h-10" disabled={pending}>
          {reject ? "Tolak" : "Setujui"}
        </Button>
      </DialogFooter>
    </form>
  );
}
