// Shared loading / empty / error views.
// 403 is shown as a denial, never as an empty list; status is always text.
import { ApiRequestError } from "../api/client";

export function Loading({ label = "Memuat..." }: { label?: string }) {
  return <p role="status" className="loading">{label}</p>;
}

export function EmptyState({ message }: { message: string }) {
  return <p className="empty-state">{message}</p>;
}

export function ErrorState({ error }: { error: unknown }) {
  if (error instanceof ApiRequestError) {
    const heading =
      error.status === 403 ? "Akses ditolak" :
      error.status === 404 ? "Tidak ditemukan" :
      error.code === "NETWORK_ERROR" ? "Server tidak dapat dihubungi" :
      "Terjadi kesalahan";
    return (
      <div role="alert" className="alert">
        <strong>{heading}</strong>
        <p>{error.message}</p>
        {error.request_id && <small>ID permintaan: {error.request_id}</small>}
      </div>
    );
  }
  return (
    <div role="alert" className="alert">
      <strong>Terjadi kesalahan</strong>
    </div>
  );
}
