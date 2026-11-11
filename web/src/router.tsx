import { QueryClient } from "@tanstack/react-query";
import { createRouter, type RouterHistory } from "@tanstack/react-router";
import { isClientError } from "@/api/client";
import { ErrorState } from "@/components/ErrorState";
import { Skeleton } from "@/components/ui/skeleton";
import { routeTree } from "./routeTree.gen";

export function createQueryClient() {
  return new QueryClient({
    defaultOptions: {
      queries: { staleTime: 10_000, retry: (failures, err) => failures < 1 && !isClientError(err) },
    },
  });
}

export function createAppRouter(queryClient: QueryClient, history?: RouterHistory) {
  return createRouter({
    routeTree,
    history,
    context: { queryClient },
    defaultPreload: "intent",
    defaultPreloadStaleTime: 0, // TanStack Query decides freshness
    scrollRestoration: true,
    defaultPendingComponent: () => (
      <div className="grid gap-3" aria-busy="true" aria-label="Memuat">
        <Skeleton className="h-8 w-1/3" />
        <Skeleton className="h-40 w-full" />
      </div>
    ),
    defaultErrorComponent: ({ error, reset }) => <ErrorState error={error} onRetry={reset} />,
  });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
