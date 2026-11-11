import { type FormEvent, useState } from "react";
import { ApiError, type Schemas } from "@/api/client";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";

type Driver = Schemas["Driver"];
type Connection = Schemas["AdminConnection"];
type PgConfig = Schemas["PostgresPublicConfig"];

export type ConnectionValues = { name: string; driver: Driver; config: Record<string, string | number> };

const PG_FIELDS = [
  { name: "host", label: "Host" },
  { name: "port", label: "Port", type: "number" },
  { name: "database", label: "Nama database" },
  { name: "username", label: "Username" },
  { name: "password", label: "Password", type: "password" },
  { name: "editor_role", label: "Role editor terbatas" },
] as const;

/**
 * Create or edit a registration. Secrets are typed here and sent once; the
 * API never returns them, so editing leaves the password empty to keep it.
 */
export function ConnectionForm({ current, onSubmit, onCancel }: {
  current?: Connection;
  onSubmit: (values: ConnectionValues) => Promise<void>;
  onCancel: () => void;
}) {
  const [driver, setDriver] = useState<Driver>(current?.driver ?? "postgres");
  const [sslmode, setSslmode] = useState((current?.config as PgConfig | undefined)?.sslmode ?? "require");
  const [error, setError] = useState<ApiError | null>(null);
  const [pending, setPending] = useState(false);
  const pg = current?.driver === "postgres" ? (current.config as PgConfig) : undefined;
  const fieldError = (key: string) => error?.fields[key];

  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = new FormData(e.currentTarget);
    const text = (k: string) => String(form.get(k) ?? "").trim();
    const config: Record<string, string | number> =
      driver === "sqlite"
        ? { file: text("file") }
        : { host: text("host"), port: Number(text("port")), database: text("database"), username: text("username"), sslmode, editor_role: text("editor_role") };
    if (driver === "postgres" && String(form.get("password")) !== "") config.password = String(form.get("password"));
    setPending(true);
    setError(null);
    try {
      await onSubmit({ name: text("name"), driver, config });
    } catch (err) {
      setError(err instanceof ApiError ? err : new ApiError({ code: "INTERNAL_ERROR", message: "Gagal menyimpan." }, 0));
    } finally {
      setPending(false);
    }
  }

  return (
    <form onSubmit={submit} className="grid gap-4">
      <Field id="conn-name" label="Nama" error={fieldError("name")}>
        <Input id="conn-name" name="name" defaultValue={current?.name} required aria-invalid={!!fieldError("name")} />
      </Field>
      {!current && (
        <div className="grid gap-1.5">
          <Label htmlFor="conn-driver">Engine</Label>
          <Select value={driver} onValueChange={(v) => setDriver(v as Driver)}>
            <SelectTrigger id="conn-driver" className="h-10 w-full">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="postgres">PostgreSQL</SelectItem>
              <SelectItem value="sqlite">SQLite</SelectItem>
            </SelectContent>
          </Select>
        </div>
      )}
      {driver === "sqlite" ? (
        <Field id="conn-file" label="Berkas SQLite (.sqlite/.db di direktori target)" error={fieldError("config.file")}>
          <Input id="conn-file" name="file" required defaultValue={current && "file" in current.config ? current.config.file : undefined}
            pattern="[A-Za-z0-9][A-Za-z0-9._\-]*\.(sqlite|db)" aria-invalid={!!fieldError("config.file")} />
        </Field>
      ) : (
        <div className="grid gap-4 sm:grid-cols-2">
          {PG_FIELDS.map((f) => {
            const secret = f.name === "password";
            const id = `conn-${f.name}`;
            return (
              <Field key={f.name} id={id} label={f.label} error={fieldError(`config.${f.name}`)}>
                <Input id={id} name={f.name} type={"type" in f ? f.type : "text"} required={!pg || !secret}
                  defaultValue={secret ? undefined : pg?.[f.name]} placeholder={pg && secret ? "kosongkan untuk mempertahankan" : undefined}
                  autoComplete={secret ? "new-password" : "off"} aria-invalid={!!fieldError(`config.${f.name}`)} />
              </Field>
            );
          })}
          <Field id="conn-sslmode" label="SSL mode" error={fieldError("config.sslmode")}>
            <Select value={sslmode} onValueChange={(v) => setSslmode(v as PgConfig["sslmode"])}>
              <SelectTrigger id="conn-sslmode" className="h-10 w-full">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="require">require</SelectItem>
                <SelectItem value="verify-ca">verify-ca</SelectItem>
                <SelectItem value="verify-full">verify-full</SelectItem>
                <SelectItem value="disable">disable (lokal saja)</SelectItem>
              </SelectContent>
            </Select>
          </Field>
        </div>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error.message}
        </p>
      )}
      <div className="flex justify-end gap-2">
        <Button type="button" variant="outline" className="h-10" onClick={onCancel} disabled={pending}>
          Batal
        </Button>
        <Button type="submit" className="h-10" disabled={pending}>
          Simpan
        </Button>
      </div>
    </form>
  );
}

function Field({ id, label, error, children }: { id: string; label: string; error?: string; children: React.ReactNode }) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {error && (
        <p id={`${id}-error`} role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </div>
  );
}
