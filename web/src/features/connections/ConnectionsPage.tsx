// Admin registry of target databases. Secrets are typed into the
// form and sent once; the API never returns them, so nothing secret is shown.
import { useState, type FormEvent, type ReactNode } from "react";
import { ApiRequestError, request } from "../../api/client";
import type { AdminConnection, Driver, PostgresPublicConfig, PublicConfig } from "../../api/types";
import { useApi } from "../../api/useApi";
import { EmptyState, ErrorState, Loading } from "../../components/StatusViews";

type Notice = { kind: "ok" | "error"; text: string } | null;

const PG_FIELDS = [
  { name: "host", label: "Host", type: "text" },
  { name: "port", label: "Port", type: "number" },
  { name: "database", label: "Nama database", type: "text" },
  { name: "username", label: "Username", type: "text" },
  { name: "password", label: "Password", type: "password" },
  { name: "editor_role", label: "Role editor terbatas", type: "text" },
] as const;

type FieldError = (key: string) => ReactNode;

function useFieldErrors(idPrefix: string) {
  const [errors, setErrors] = useState<Record<string, string>>({});
  const fieldError: FieldError = (key) =>
    errors[key] ? (
      <span id={`${idPrefix}${key}`} role="alert">
        {errors[key]}
      </span>
    ) : null;
  return [setErrors, fieldError] as const;
}

// ConfigFields renders the per-driver config inputs. With current set (edit),
// fields start from the stored public config and the password may stay empty
// to keep the stored secret.
function ConfigFields({ driver, current, idPrefix, fieldError }: {
  driver: Driver; current?: PublicConfig; idPrefix: string; fieldError: FieldError;
}) {
  if (driver === "sqlite") {
    return (
      <div>
        <label>
          Berkas SQLite (.sqlite/.db di direktori target){" "}
          <input
            name="file"
            required
            defaultValue={current && "file" in current ? current.file : undefined}
            pattern="[A-Za-z0-9][A-Za-z0-9._\-]*\.(sqlite|db)"
            aria-describedby={`${idPrefix}config.file`}
          />
        </label>
        {fieldError("config.file")}
      </div>
    );
  }
  const pg = current as PostgresPublicConfig | undefined;
  return (
    <>
      {PG_FIELDS.map((fd) => {
        const secret = fd.name === "password";
        return (
          <div key={fd.name}>
            <label>
              {fd.label}{" "}
              <input
                name={fd.name}
                type={fd.type}
                required={!pg || !secret}
                defaultValue={fd.name === "password" ? undefined : pg?.[fd.name]}
                placeholder={pg && secret ? "kosongkan untuk mempertahankan" : undefined}
                autoComplete={secret ? "new-password" : "off"}
                aria-describedby={`${idPrefix}config.${fd.name}`}
              />
            </label>
            {fieldError(`config.${fd.name}`)}
          </div>
        );
      })}
      <label>
        SSL mode{" "}
        <select name="sslmode" defaultValue={pg?.sslmode ?? "require"}>
          <option value="require">require</option>
          <option value="verify-ca">verify-ca</option>
          <option value="verify-full">verify-full</option>
          <option value="disable">disable (lokal saja)</option>
        </select>
      </label>
      {fieldError("config.sslmode")}
    </>
  );
}

function CreateForm({ onCreated }: { onCreated: () => void }) {
  const [driver, setDriver] = useState<Driver>("postgres");
  const [setErrors, fieldError] = useFieldErrors("err-");
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const form = e.currentTarget;
    const f = new FormData(form);
    const config =
      driver === "postgres"
        ? {
            host: String(f.get("host") ?? ""),
            port: Number(f.get("port")),
            database: String(f.get("database") ?? ""),
            username: String(f.get("username") ?? ""),
            password: String(f.get("password") ?? ""),
            sslmode: String(f.get("sslmode") ?? "require"),
            editor_role: String(f.get("editor_role") ?? ""),
          }
        : { file: String(f.get("file") ?? "") };
    setBusy(true);
    setErrors({});
    setFailure(null);
    try {
      await request<AdminConnection>("/admin/connections", {
        method: "POST",
        body: { name: String(f.get("name") ?? ""), driver, config },
      });
      form.reset(); // drop the typed password from the form right away
      onCreated();
    } catch (err) {
      if (err instanceof ApiRequestError && Object.keys(err.fields).length > 0) setErrors(err.fields);
      else setFailure(err);
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} aria-label="Daftarkan koneksi">
      <h2>Daftarkan koneksi</h2>
      <label>
        Nama <input name="name" required maxLength={100} aria-describedby="err-name" />
      </label>
      {fieldError("name")}
      <label>
        Driver{" "}
        <select name="driver" value={driver} onChange={(e) => setDriver(e.target.value as Driver)}>
          <option value="postgres">PostgreSQL</option>
          <option value="sqlite">SQLite</option>
        </select>
      </label>
      <ConfigFields driver={driver} idPrefix="err-" fieldError={fieldError} />
      {fieldError("config")}
      {failure !== null && <ErrorState error={failure} />}
      <button type="submit" disabled={busy}>
        {busy ? "Menyimpan..." : "Daftarkan"}
      </button>
    </form>
  );
}

// EditForm sends PATCH with the revision it was opened at and only the fields
// that changed; an empty password keeps the stored secret.
function EditForm({ conn, onDone, onCancel }: { conn: AdminConnection; onDone: (msg: string) => void; onCancel: () => void }) {
  const [setErrors, fieldError] = useFieldErrors("edit-err-");
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    const text = (k: string) => String(f.get(k) ?? "");
    const config: Record<string, string | number> = {};
    if (conn.driver === "postgres") {
      const pg = conn.config as PostgresPublicConfig;
      for (const k of ["host", "database", "username", "sslmode", "editor_role"] as const) {
        if (text(k) !== pg[k]) config[k] = text(k);
      }
      if (Number(f.get("port")) !== pg.port) config.port = Number(f.get("port"));
      if (text("password") !== "") config.password = text("password");
    } else if ("file" in conn.config && text("file") !== conn.config.file) {
      config.file = text("file");
    }
    const body: { revision: number; name?: string; config?: typeof config } = { revision: conn.revision };
    if (text("name") !== conn.name) body.name = text("name");
    if (Object.keys(config).length > 0) body.config = config;
    if (body.name === undefined && body.config === undefined) {
      onDone(`${conn.name}: tidak ada perubahan.`);
      return;
    }
    setBusy(true);
    setErrors({});
    setFailure(null);
    try {
      await request<AdminConnection>(`/admin/connections/${conn.id}`, { method: "PATCH", body });
      onDone(`${body.name ?? conn.name} diperbarui.`);
    } catch (err) {
      if (err instanceof ApiRequestError && Object.keys(err.fields).length > 0) setErrors(err.fields);
      else setFailure(err); // REVISION_CONFLICT included: its message asks for a reload
    } finally {
      setBusy(false);
    }
  }

  return (
    <form onSubmit={submit} aria-label={`Ubah koneksi ${conn.name}`}>
      <h2>Ubah koneksi {conn.name}</h2>
      <label>
        Nama <input name="name" required maxLength={100} defaultValue={conn.name} aria-describedby="edit-err-name" />
      </label>
      {fieldError("name")}
      <p>Driver: {conn.driver} (tidak dapat diubah)</p>
      <ConfigFields driver={conn.driver} current={conn.config} idPrefix="edit-err-" fieldError={fieldError} />
      {fieldError("config")}
      {failure !== null && <ErrorState error={failure} />}
      <button type="submit" disabled={busy}>
        {busy ? "Menyimpan..." : "Simpan perubahan"}
      </button>{" "}
      <button type="button" onClick={onCancel}>
        Batal
      </button>
    </form>
  );
}

function statusText(c: AdminConnection) {
  if (!c.active) return "Nonaktif";
  if (c.migration_ready) return "Aktif, siap migrasi";
  if (c.last_error_code) return `Aktif, uji terakhir gagal (${c.last_error_code})`;
  return "Aktif, belum diuji";
}

function target(c: AdminConnection) {
  if (c.driver === "sqlite") return "file" in c.config ? c.config.file : "";
  const p = c.config as PostgresPublicConfig;
  return `${p.username}@${p.host}:${p.port}/${p.database} (sslmode ${p.sslmode})`;
}

export function ConnectionsPage() {
  const [q, reload] = useApi<AdminConnection[]>("/admin/connections?limit=200");
  const [notice, setNotice] = useState<Notice>(null);
  const [pending, setPending] = useState<string | null>(null);
  const [editing, setEditing] = useState<AdminConnection | null>(null);

  async function act(c: AdminConnection, action: "test" | "deactivate") {
    if (action === "deactivate" && !window.confirm(`Nonaktifkan ${c.name}? Akses pengguna ke database ini dicabut.`)) return;
    setPending(c.id);
    setNotice(null);
    try {
      if (action === "test") {
        const res = await request<{ reachable: boolean; latency_ms: number }>(`/admin/connections/${c.id}/test`, {
          method: "POST",
          body: {},
        });
        setNotice({ kind: "ok", text: `${c.name}: terhubung (${res!.data.latency_ms} ms), ledger siap.` });
      } else {
        await request(`/admin/connections/${c.id}`, { method: "DELETE" });
        setNotice({ kind: "ok", text: `${c.name} dinonaktifkan.` });
      }
    } catch (err) {
      const msg = err instanceof ApiRequestError ? `${err.message} (${err.code})` : "Terjadi kesalahan.";
      setNotice({ kind: "error", text: `${c.name}: ${msg}` });
    } finally {
      setPending(null);
      reload();
    }
  }

  return (
    <section>
      <h1>Koneksi database</h1>
      {notice && <p role={notice.kind === "error" ? "alert" : "status"}>{notice.text}</p>}
      {q.status === "loading" && <Loading />}
      {q.status === "error" && <ErrorState error={q.error} />}
      {q.status === "success" &&
        (q.response.data.length === 0 ? (
          <EmptyState message="Belum ada koneksi terdaftar." />
        ) : (
          <div style={{ overflowX: "auto" }}>
            <table>
              <thead>
                <tr>
                  <th scope="col">Nama</th>
                  <th scope="col">Driver</th>
                  <th scope="col">Target</th>
                  <th scope="col">Status</th>
                  <th scope="col">Aksi</th>
                </tr>
              </thead>
              <tbody>
                {q.response.data.map((c) => (
                  <tr key={c.id}>
                    <td>{c.name}</td>
                    <td>{c.driver}</td>
                    <td>{target(c)}</td>
                    <td>{statusText(c)}</td>
                    <td>
                      {c.active && (
                        <>
                          <button type="button" disabled={pending === c.id} onClick={() => setEditing(c)}>
                            Ubah
                          </button>{" "}
                          <button type="button" disabled={pending === c.id} onClick={() => act(c, "test")}>
                            Uji koneksi
                          </button>{" "}
                          <button type="button" disabled={pending === c.id} onClick={() => act(c, "deactivate")}>
                            Nonaktifkan
                          </button>
                        </>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        ))}
      {editing && (
        <EditForm
          key={`${editing.id}:${editing.revision}`}
          conn={editing}
          onCancel={() => setEditing(null)}
          onDone={(text) => {
            setEditing(null);
            setNotice({ kind: "ok", text });
            reload();
          }}
        />
      )}
      <CreateForm onCreated={reload} />
    </section>
  );
}
