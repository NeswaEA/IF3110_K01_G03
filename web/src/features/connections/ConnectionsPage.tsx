// Admin registry of target databases. Secrets are typed into the
// form and sent once; the API never returns them, so nothing secret is shown.
import { useState, type FormEvent } from "react";
import { ApiRequestError, request } from "../../api/client";
import type { AdminConnection, Driver, PostgresPublicConfig } from "../../api/types";
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

function CreateForm({ onCreated }: { onCreated: () => void }) {
  const [driver, setDriver] = useState<Driver>("postgres");
  const [errors, setErrors] = useState<Record<string, string>>({});
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

  const fieldError = (key: string) =>
    errors[key] ? (
      <span id={`err-${key}`} role="alert">
        {errors[key]}
      </span>
    ) : null;

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
      {driver === "postgres" ? (
        <>
          {PG_FIELDS.map((fd) => (
            <div key={fd.name}>
              <label>
                {fd.label}{" "}
                <input
                  name={fd.name}
                  type={fd.type}
                  required
                  autoComplete={fd.type === "password" ? "new-password" : "off"}
                  aria-describedby={`err-config.${fd.name}`}
                />
              </label>
              {fieldError(`config.${fd.name}`)}
            </div>
          ))}
          <label>
            SSL mode{" "}
            <select name="sslmode" defaultValue="require">
              <option value="require">require</option>
              <option value="verify-ca">verify-ca</option>
              <option value="verify-full">verify-full</option>
              <option value="disable">disable (lokal saja)</option>
            </select>
          </label>
          {fieldError("config.sslmode")}
        </>
      ) : (
        <div>
          <label>
            Berkas SQLite (.sqlite/.db di direktori target){" "}
            <input name="file" required pattern="[A-Za-z0-9][A-Za-z0-9._\-]*\.(sqlite|db)" aria-describedby="err-config.file" />
          </label>
          {fieldError("config.file")}
        </div>
      )}
      {fieldError("config")}
      {failure !== null && <ErrorState error={failure} />}
      <button type="submit" disabled={busy}>
        {busy ? "Menyimpan..." : "Daftarkan"}
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
      <CreateForm onCreated={reload} />
    </section>
  );
}
