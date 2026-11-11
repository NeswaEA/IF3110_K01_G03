import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, Outlet, redirect, useLocation, useNavigate, useParams, useRouter } from "@tanstack/react-router";
import { Database, LogOut, Server, Table2 } from "lucide-react";
import { useEffect, useState } from "react";
import { toast } from "sonner";
import { ApiError, onUnauthorized } from "@/api/client";
import { useDatabaseEvents } from "@/api/events";
import { logout, meQuery } from "@/api/queries/auth";
import { databaseQuery } from "@/api/queries/databases";
import { ReLoginDialog } from "@/components/ReLoginDialog";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Separator } from "@/components/ui/separator";
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarInset,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
  SidebarTrigger,
  useSidebar,
} from "@/components/ui/sidebar";

export const Route = createFileRoute("/_app")({
  // Convenience only: every API call checks the session again.
  beforeLoad: async ({ context, location }) => {
    try {
      return { me: await context.queryClient.ensureQueryData(meQuery) };
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        throw redirect({ to: "/login", search: { next: location.href } });
      }
      throw err;
    }
  },
  component: AppLayout,
});

function AppLayout() {
  const { databaseId } = useParams({ strict: false });
  const reLogin = useSessionExpiry();
  useDatabaseEvents(databaseId);
  return (
    <SidebarProvider>
      <AppSidebar databaseId={databaseId} />
      <SidebarInset className="min-w-0">
        <header className="sticky top-0 z-20 flex h-14 items-center gap-2 border-b bg-background px-4">
          <SidebarTrigger className="size-10" aria-label="Buka atau tutup navigasi" />
          <Separator orientation="vertical" className="h-6" />
          {databaseId ? <ActiveDatabase id={databaseId} /> : <span className="font-medium">Platform Database</span>}
        </header>
        <main id="main" className="min-w-0 flex-1 p-4 lg:p-6">
          <Outlet />
        </main>
      </SidebarInset>
      <ReLoginDialog {...reLogin} />
    </SidebarProvider>
  );
}

/**
 * On a 401 the user is sent to the login page, unless the current route
 * blocks navigation (unsaved changes): then a dialog signs them in in place.
 */
function useSessionExpiry() {
  const router = useRouter();
  const navigate = useNavigate();
  const location = useLocation();
  const queryClient = useQueryClient();
  const [open, setOpen] = useState(false);

  useEffect(
    () =>
      onUnauthorized(() => {
        queryClient.removeQueries({ queryKey: meQuery.queryKey });
        if (router.history._getBlockers().length > 0) {
          setOpen(true);
        } else {
          void navigate({ to: "/login", search: { next: router.state.location.href } });
        }
      }),
    [router, navigate, queryClient],
  );

  return {
    open,
    onSignedIn: () => setOpen(false),
    onLeave: () => {
      setOpen(false);
      void navigate({ to: "/login", search: { next: location.href }, ignoreBlocker: true });
    },
  };
}

function ActiveDatabase({ id }: { id: string }) {
  const { data } = useQuery(databaseQuery(id));
  if (!data) return <span className="text-muted-foreground">Memuat database…</span>;
  return (
    <div className="flex min-w-0 items-center gap-2">
      <span className="truncate font-medium" title={data.name}>
        {data.name}
      </span>
      <Badge variant="outline">{data.driver === "postgres" ? "PostgreSQL" : "SQLite"}</Badge>
      <Badge variant="secondary" className="hidden sm:inline-flex">
        {data.effective_access === "edit" ? "Akses edit" : "Akses lihat"}
      </Badge>
    </div>
  );
}

function AppSidebar({ databaseId }: { databaseId?: string }) {
  const { me } = Route.useRouteContext();
  const { setOpenMobile } = useSidebar();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const close = () => setOpenMobile(false);

  async function signOut() {
    try {
      await logout();
    } catch {
      toast.error("Logout gagal; sesi tetap diakhiri di perangkat ini.");
    }
    queryClient.clear();
    await navigate({ to: "/login" });
  }

  return (
    <Sidebar>
      <SidebarHeader className="px-4 py-3 font-semibold">Platform Database</SidebarHeader>
      <SidebarContent>
        <SidebarGroup>
          <SidebarGroupContent>
            <SidebarMenu>
              <NavItem to="/databases" icon={Database} label="Daftar database" onNavigate={close} exact />
            </SidebarMenu>
          </SidebarGroupContent>
        </SidebarGroup>
        {databaseId && (
          <SidebarGroup>
            <SidebarGroupLabel>Database aktif</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <SidebarMenuItem>
                  <SidebarMenuButton asChild className="h-10">
                    <Link to="/databases/$databaseId" params={{ databaseId }} onClick={close} activeOptions={{ exact: true, includeSearch: false }} activeProps={{ "data-active": true }}>
                      <Table2 aria-hidden />
                      <span>Schema &amp; data</span>
                    </Link>
                  </SidebarMenuButton>
                </SidebarMenuItem>
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
        {me.is_platform_admin && (
          <SidebarGroup>
            <SidebarGroupLabel>Administrasi</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                <NavItem to="/admin/connections" icon={Server} label="Koneksi database" onNavigate={close} />
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        )}
      </SidebarContent>
      <SidebarFooter className="gap-2 p-3">
        <p className="truncate px-1 text-sm text-muted-foreground" title={me.username}>
          Masuk sebagai <span className="font-medium text-foreground">{me.username}</span>
        </p>
        <Button type="button" variant="outline" className="h-10 justify-start" onClick={signOut}>
          <LogOut aria-hidden />
          Logout
        </Button>
      </SidebarFooter>
    </Sidebar>
  );
}

function NavItem({ to, icon: Icon, label, onNavigate, exact }: {
  to: "/databases" | "/admin/connections";
  icon: typeof Database;
  label: string;
  onNavigate: () => void;
  exact?: boolean;
}) {
  return (
    <SidebarMenuItem>
      <SidebarMenuButton asChild className="h-10">
        <Link to={to} onClick={onNavigate} activeOptions={{ exact, includeSearch: false }} activeProps={{ "data-active": true }}>
          <Icon aria-hidden />
          <span>{label}</span>
        </Link>
      </SidebarMenuButton>
    </SidebarMenuItem>
  );
}
