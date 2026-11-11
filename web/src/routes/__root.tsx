import type { QueryClient } from "@tanstack/react-query";
import { createRootRouteWithContext, Link, Outlet } from "@tanstack/react-router";
import { FileQuestion } from "lucide-react";
import { EmptyState } from "@/components/EmptyState";
import { Button } from "@/components/ui/button";
import { Toaster } from "@/components/ui/sonner";
import { TooltipProvider } from "@/components/ui/tooltip";

export const Route = createRootRouteWithContext<{ queryClient: QueryClient }>()({
  component: () => (
    <TooltipProvider>
      <Outlet />
      <Toaster position="top-center" />
    </TooltipProvider>
  ),
  notFoundComponent: () => (
    <main className="grid min-h-[60svh] place-items-center p-4">
      <EmptyState
        icon={FileQuestion}
        title="Halaman tidak ditemukan"
        description="Alamat ini tidak ada atau sudah dipindahkan."
        action={
          <Button asChild>
            <Link to="/">Ke halaman utama</Link>
          </Button>
        }
      />
    </main>
  ),
});
