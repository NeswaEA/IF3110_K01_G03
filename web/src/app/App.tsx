import type { ReactNode } from "react";
import { BrowserRouter, Link, Navigate, Outlet, Route, Routes } from "react-router-dom";
import { Loading } from "../components/StatusViews";
import { ConnectionsPage } from "../features/connections/ConnectionsPage";
import { DatabaseList } from "../features/viewer/DatabaseList";
import { DatabaseViewer } from "../features/viewer/DatabaseViewer";
import { SessionProvider, useSession } from "./session";

// Feature pages are owned by their authors and plug into these routes:
// auth/access -> neswa, connections/viewer/explain -> samson, migrations/schemadiff -> argha.
function Pending({ feature }: { feature: string }) {
  return <p>Halaman {feature} belum tersedia.</p>;
}

export function RequireAuth() {
  const { status } = useSession();
  if (status === "loading") return <Loading />;
  if (status === "anonymous") return <Navigate to="/login" replace />;
  return <Layout />;
}

function Layout() {
  const { user } = useSession();
  return (
    <>
      <header>
        <nav aria-label="Navigasi utama">
          <Link to="/databases">Database</Link>
          {user?.is_platform_admin && <Link to="/admin">Administrasi</Link>}
          {user?.is_platform_admin && <Link to="/admin/connections">Koneksi</Link>}
        </nav>
        <span>{user?.username}</span>
      </header>
      <main>
        <Outlet />
      </main>
    </>
  );
}

export function AppRoutes(): ReactNode {
  return (
    <Routes>
      <Route path="/login" element={<Pending feature="login" />} />
      <Route element={<RequireAuth />}>
        <Route index element={<Navigate to="/databases" replace />} />
        <Route path="/databases" element={<DatabaseList />} />
        <Route path="/databases/:id" element={<DatabaseViewer />} />
        <Route path="/admin/connections" element={<ConnectionsPage />} />
        <Route path="/admin/*" element={<Pending feature="administrasi" />} />
      </Route>
      <Route path="*" element={<p>Halaman tidak ditemukan.</p>} />
    </Routes>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <SessionProvider>
        <AppRoutes />
      </SessionProvider>
    </BrowserRouter>
  );
}
