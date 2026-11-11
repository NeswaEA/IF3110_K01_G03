import { type FormEvent, type ReactNode, useState } from "react";
import { ApiError } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";

type Submit<T> = (values: T) => Promise<void>;

/** Runs submit and keeps the API error so each field can show its own message. */
function useSubmit<T>(onSubmit: Submit<T>) {
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

export function CreateUserForm({ onSubmit, onCancel }: {
  onSubmit: Submit<{ username: string; password: string; is_platform_admin: boolean }>;
  onCancel: () => void;
}) {
  const { error, pending, run } = useSubmit(onSubmit);
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    void run({ username: String(form.get("username") ?? "").trim(), password: String(form.get("password") ?? ""), is_platform_admin: form.get("is_platform_admin") === "on" });
  }
  return (
    <form onSubmit={submit} className="grid gap-4">
      <Field id="user-username" label="Username" hint="3-64 huruf, angka, titik, garis bawah, atau tanda hubung." error={error?.fields.username}>
        <Input id="user-username" aria-describedby="user-username-help" name="username" required autoComplete="off" aria-invalid={!!error?.fields.username} />
      </Field>
      <Field id="user-password" label="Password awal" hint="12-128 karakter. Sampaikan ke pengguna lewat jalur yang aman." error={error?.fields.password}>
        <Input id="user-password" aria-describedby="user-password-help" name="password" type="password" required minLength={12} maxLength={128} autoComplete="new-password" aria-invalid={!!error?.fields.password} />
      </Field>
      <div className="flex min-h-10 items-center gap-2">
        <input id="user-admin" name="is_platform_admin" type="checkbox" className="size-4 accent-primary" />
        <Label htmlFor="user-admin">Admin platform</Label>
      </div>
      <FormFooter error={error} pending={pending} onCancel={onCancel} submitLabel="Buat pengguna" />
    </form>
  );
}

export function ResetPasswordForm({ username, onSubmit, onCancel }: { username: string; onSubmit: Submit<string>; onCancel: () => void }) {
  const { error, pending, run } = useSubmit(onSubmit);
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    void run(String(new FormData(e.currentTarget).get("password") ?? ""));
  }
  return (
    <form onSubmit={submit} className="grid gap-4">
      <Field id="reset-password" label={`Password baru untuk ${username}`} hint="Semua sesi pengguna ini akan diakhiri." error={error?.fields.password}>
        <Input id="reset-password" aria-describedby="reset-password-help" name="password" type="password" required minLength={12} maxLength={128} autoComplete="new-password" aria-invalid={!!error?.fields.password} />
      </Field>
      <FormFooter error={error} pending={pending} onCancel={onCancel} submitLabel="Ganti password" />
    </form>
  );
}

function FormFooter({ error, pending, onCancel, submitLabel }: { error: ApiError | null; pending: boolean; onCancel: () => void; submitLabel: string }) {
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

function Field({ id, label, hint, error, children }: { id: string; label: string; hint: string; error?: string; children: ReactNode }) {
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
