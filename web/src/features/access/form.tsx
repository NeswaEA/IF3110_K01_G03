import { type ReactNode, useState } from "react";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Label } from "@/components/ui/label";

export type Submit<T> = (values: T) => Promise<void>;

/** Runs submit and keeps the API error so each field can show its own message. */
export function useSubmit<T>(onSubmit: Submit<T>) {
  const [error, setError] = useState<ApiError | null>(null);
  const [pending, setPending] = useState(false);
  async function run(values: T) {
    setPending(true);
    setError(null);
    try {
      await onSubmit(values);
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError({ code: "INTERNAL_ERROR", message: "Gagal menyimpan." }, 0));
    } finally {
      setPending(false);
    }
  }
  return { error, pending, run };
}

export function FormFooter({ error, pending, onCancel, submitLabel }: { error: ApiError | null; pending: boolean; onCancel: () => void; submitLabel: string }) {
  return (
    <>
      {error && Object.keys(error.fields).length === 0 && (
        <p role="alert" className="text-sm text-destructive">
          {error.message}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" className="h-10" onClick={onCancel} disabled={pending}>
          Batal
        </Button>
        <Button type="submit" className="h-10" disabled={pending}>
          {submitLabel}
        </Button>
      </div>
    </>
  );
}

export function Field({ id, label, hint, error, children }: { id: string; label: string; hint: string; error?: string; children: ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error ? (
        <p id={`${id}-help`} role="alert" className="text-sm text-destructive">
          {error}
        </p>
      ) : (
        <p id={`${id}-help`} className="text-sm text-muted-foreground">
          {hint}
        </p>
      )}
    </div>
  );
}
