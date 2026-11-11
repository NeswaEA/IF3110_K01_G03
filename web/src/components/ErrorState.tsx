import { AlertTriangle } from "lucide-react";
import { ApiError } from "@/api/client";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";

const titles: Record<string, string> = {
  FORBIDDEN: "Akses ditolak",
  NOT_FOUND: "Tidak ditemukan",
  UNAUTHENTICATED: "Sesi berakhir",
  NETWORK_ERROR: "Server tidak dapat dihubungi",
  TARGET_UNAVAILABLE: "Database target tidak dapat dihubungi",
  TARGET_TIMEOUT: "Database target tidak merespons",
  TARGET_NOT_READY: "Database target belum siap",
};

/** Explains a failed request: its code, the server's message and the request id to quote. */
export function ErrorState({ error, onRetry }: { error: unknown; onRetry?: () => void }) {
  const api = error instanceof ApiError ? error : null;
  const title = (api && titles[api.code]) ?? "Terjadi kesalahan";
  const message = api?.message || "Permintaan gagal. Coba lagi.";
  return (
    <Alert variant="destructive" role="alert">
      <AlertTriangle aria-hidden />
      <AlertTitle>{title}</AlertTitle>
      <AlertDescription>
        <p>{message}</p>
        {api?.requestId && <p className="text-xs">ID permintaan: {api.requestId}</p>}
        {onRetry && (
          <Button type="button" variant="outline" size="sm" className="mt-2" onClick={onRetry}>
            Coba lagi
          </Button>
        )}
      </AlertDescription>
    </Alert>
  );
}
