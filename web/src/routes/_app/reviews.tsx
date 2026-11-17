import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ClipboardCheck } from "lucide-react";
import { useDatabaseEvents } from "@/api/events";
import { REVIEWS_PAGE_SIZE, reviewsQuery } from "@/api/queries/reviews";
import type { Schemas } from "@/api/client";
import { EmptyState } from "@/components/EmptyState";
import { PageHeader } from "@/components/PageHeader";
import { Button } from "@/components/ui/button";
import { ApprovalPanel } from "@/features/reviews/ApprovalPanel";

const actions: Record<Schemas["RequestAction"], string> = { apply: "Terapkan", rollback: "Rollback", forward: "Forward" };

export const Route = createFileRoute("/_app/reviews")({
  validateSearch: (search: Record<string, unknown>): { offset?: number } => {
    const offset = Number(search.offset);
    return Number.isInteger(offset) && offset > 0 ? { offset } : {};
  },
  loaderDeps: ({ search }) => ({ offset: search.offset ?? 0 }),
  loader: ({ context, deps }) => context.queryClient.ensureQueryData(reviewsQuery(deps.offset)),
  component: ReviewsPage,
});

function ReviewsPage() {
  const offset = Route.useSearch().offset ?? 0;
  const { data } = useSuspenseQuery(reviewsQuery(offset));
  const databases = [...new Set(data.data.map((item) => item.database.id))];

  return (
    <>
      <PageHeader
        title="Menunggu persetujuan"
        description="Perubahan pada database yang grupnya Anda pimpin dan belum Anda putuskan. Satu keputusan berlaku untuk semua grup Anda pada permintaan itu."
      />
      {databases.map((id) => (
        <DatabaseEvents key={id} databaseId={id} />
      ))}
      {data.data.length === 0 ? (
        <EmptyState
          icon={ClipboardCheck}
          title="Tidak ada yang menunggu keputusan Anda"
          description="Permintaan baru muncul di sini saat anggota grup yang Anda pimpin mengajukan perubahan."
        />
      ) : (
        <ul className="grid gap-6">
          {data.data.map((item) => (
            <li key={item.request.id} className="grid gap-3">
              <div className="min-w-0">
                <h2 className="text-lg font-semibold break-words"><Link className="underline-offset-4 hover:underline"
                  to="/databases/$databaseId/migrations/$migrationId"
                  params={{databaseId:item.database.id,migrationId:item.request.migration_id}} search={{tab:"revisions"}}>{item.request.migration_title}</Link></h2>
                <p className="text-sm text-muted-foreground break-words">
                  {item.database.name} · {actions[item.request.action]} revisi {item.request.migration_revision} · diajukan{" "}
                  {new Date(item.request.created_at).toLocaleString("id-ID")}
                </p>
              </div>
              <ApprovalPanel request={item.request} />
            </li>
          ))}
        </ul>
      )}
      {(offset > 0 || data.page.has_more) && (
        <nav aria-label="Halaman daftar persetujuan" className="mt-6 flex justify-end gap-2">
          {offset > 0 && (
            <Button asChild variant="outline" className="h-10">
              <Link to="/reviews" search={{ offset: Math.max(0, offset - REVIEWS_PAGE_SIZE) || undefined }}>
                Sebelumnya
              </Link>
            </Button>
          )}
          {data.page.has_more && (
            <Button asChild variant="outline" className="h-10">
              <Link to="/reviews" search={{ offset: offset + REVIEWS_PAGE_SIZE }}>
                Berikutnya
              </Link>
            </Button>
          )}
        </nav>
      )}
    </>
  );
}

/** Listens to one database's events so decisions made elsewhere refresh this inbox. */
function DatabaseEvents({ databaseId }: { databaseId: string }) {
  useDatabaseEvents(databaseId);
  return null;
}
