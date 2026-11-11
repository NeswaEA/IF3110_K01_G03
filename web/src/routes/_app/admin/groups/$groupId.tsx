import { useSuspenseQuery } from "@tanstack/react-query";
import { createFileRoute, Link, notFound, useNavigate } from "@tanstack/react-router";
import { ArrowLeft } from "lucide-react";
import { connectionsQuery } from "@/api/queries/connections";
import { allUsersQuery, groupGrantsQuery, groupMembersQuery, groupsQuery } from "@/api/queries/groups";
import { PageHeader } from "@/components/PageHeader";
import { Button } from "@/components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { GrantsTab, LeaderTab, MembersTab } from "@/features/access/GroupTabs";

type Tab = "members" | "leader" | "grants";

export const Route = createFileRoute("/_app/admin/groups/$groupId")({
  validateSearch: (s: Record<string, unknown>): { tab?: Tab } => ({ tab: s.tab === "leader" || s.tab === "grants" ? s.tab : undefined }),
  loader: async ({ context: { queryClient }, params: { groupId } }) => {
    const groups = await queryClient.ensureQueryData(groupsQuery);
    if (!groups.some((g) => g.id === groupId)) throw notFound();
    await Promise.all([
      queryClient.ensureQueryData(groupMembersQuery(groupId)),
      queryClient.ensureQueryData(groupGrantsQuery(groupId)),
      queryClient.ensureQueryData(allUsersQuery),
      queryClient.ensureQueryData(connectionsQuery),
    ]);
  },
  component: GroupPage,
});

function GroupPage() {
  const { groupId } = Route.useParams();
  const tab = Route.useSearch().tab ?? "members";
  const navigate = useNavigate({ from: Route.fullPath });
  const group = useSuspenseQuery(groupsQuery).data.find((g) => g.id === groupId);
  if (!group) throw notFound(); // deleted from another tab

  return (
    <>
      <Button asChild variant="ghost" className="mb-2 h-10 px-2">
        <Link to="/admin/groups">
          <ArrowLeft aria-hidden />
          Semua grup
        </Link>
      </Button>
      <PageHeader title={group.name} description={group.leader ? `Leader: ${group.leader.username}` : "Belum ada leader. Grup ini belum bisa diberi hak akses database."} />
      <Tabs value={tab} onValueChange={(v) => void navigate({ search: { tab: v === "members" ? undefined : (v as Tab) } })}>
        <TabsList className="h-10">
          <TabsTrigger value="members">Anggota</TabsTrigger>
          <TabsTrigger value="leader">Leader</TabsTrigger>
          <TabsTrigger value="grants">Hak akses</TabsTrigger>
        </TabsList>
        <TabsContent value="members" className="mt-4">
          <MembersTab group={group} />
        </TabsContent>
        <TabsContent value="leader" className="mt-4">
          <LeaderTab group={group} />
        </TabsContent>
        <TabsContent value="grants" className="mt-4">
          <GrantsTab group={group} />
        </TabsContent>
      </Tabs>
    </>
  );
}
