import {
  Ban,
  CheckCircle2,
  CircleDashed,
  CircleHelp,
  Clock,
  Eye,
  Loader2,
  type LucideIcon,
  PauseCircle,
  Play,
  Replace,
  SplitSquareHorizontal,
  ThumbsUp,
  Undo2,
  XCircle,
} from "lucide-react";
import type { Schemas } from "@/api/client";
import { Badge } from "@/components/ui/badge";
import { cn } from "@/lib/utils";

type Tone = "neutral" | "info" | "success" | "warning" | "danger";
type Look = { label: string; icon: LucideIcon; tone: Tone; spin?: boolean };

// Status is always text plus icon; colour only reinforces it.
const tones: Record<Tone, string> = {
  neutral: "border-border bg-muted text-foreground",
  info: "border-sky-300 bg-sky-50 text-sky-900",
  success: "border-emerald-300 bg-emerald-50 text-emerald-900",
  warning: "border-amber-300 bg-amber-50 text-amber-900",
  danger: "border-red-300 bg-red-50 text-red-900",
};

const execution: Record<Schemas["ExecutionStatus"], Look> = {
  queued: { label: "Dalam antrean", icon: Clock, tone: "info" },
  held: { label: "Ditahan", icon: PauseCircle, tone: "warning" },
  running: { label: "Berjalan", icon: Loader2, tone: "info", spin: true },
  succeeded: { label: "Berhasil", icon: CheckCircle2, tone: "success" },
  failed: { label: "Gagal", icon: XCircle, tone: "danger" },
  partial: { label: "Sebagian", icon: SplitSquareHorizontal, tone: "warning" },
  partially_reverted: { label: "Sebagian dibatalkan", icon: Undo2, tone: "warning" },
  outcome_unknown: { label: "Hasil belum pasti", icon: CircleHelp, tone: "danger" },
  cancelled: { label: "Dibatalkan", icon: Ban, tone: "neutral" },
};

const request: Record<Schemas["RequestStatus"], Look> = {
  pending_review: { label: "Menunggu review", icon: Eye, tone: "info" },
  approved: { label: "Disetujui", icon: ThumbsUp, tone: "success" },
  rejected: { label: "Ditolak", icon: XCircle, tone: "danger" },
  cancelled: { label: "Dibatalkan", icon: Ban, tone: "neutral" },
  executed: { label: "Dijalankan", icon: Play, tone: "neutral" },
};

const migration: Record<Schemas["MigrationStatus"], Look> = {
  pending: { label: "Belum diterapkan", icon: CircleDashed, tone: "neutral" },
  applied: { label: "Diterapkan", icon: CheckCircle2, tone: "success" },
  rolled_back: { label: "Di-rollback", icon: Undo2, tone: "warning" },
  superseded: { label: "Tergantikan", icon: Replace, tone: "neutral" },
  partial: { label: "Sebagian", icon: SplitSquareHorizontal, tone: "warning" },
};

type Props =
  | { kind: "execution"; status: Schemas["ExecutionStatus"]; className?: string }
  | { kind: "request"; status: Schemas["RequestStatus"]; className?: string }
  | { kind: "migration"; status: Schemas["MigrationStatus"]; className?: string };

export function statusLabel(p: Pick<Props, "kind" | "status">): string {
  return lookOf(p).label;
}

function lookOf({ kind, status }: Pick<Props, "kind" | "status">): Look {
  const table: Record<string, Look> = kind === "execution" ? execution : kind === "request" ? request : migration;
  return table[status] ?? { label: status, icon: CircleHelp, tone: "neutral" };
}

export function StatusBadge(props: Props) {
  const look = lookOf(props);
  const Icon = look.icon;
  return (
    <Badge variant="outline" className={cn("gap-1 font-medium", tones[look.tone], props.className)} data-status={props.status}>
      <Icon aria-hidden className={cn("size-3.5", look.spin && "animate-spin motion-reduce:animate-none")} />
      {look.label}
    </Badge>
  );
}
