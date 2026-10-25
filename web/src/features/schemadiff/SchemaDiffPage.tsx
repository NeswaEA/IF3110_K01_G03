import { useEffect, useRef, useState, type FormEvent } from "react";
import { ApiRequestError, isAbortError, request } from "../../api/client";
import type { DatabaseSummary, DiffChangeKind, DiffResult, MigrationDraft } from "../../api/types";
import { loginUrl } from "../../app/mount";
import { ErrorState } from "../../components/StatusViews";
import { SQLEditor } from "../migrations/SQLEditor";
import { useUnsavedChanges } from "../migrations/useUnsavedChanges";
import "../migrations/migrations.css";
import "./schemadiff.css";

export type DiffInitialState = {
  database: DatabaseSummary;
  comparisons: DatabaseSummary[];
  schemas: string[];
  desired_database_id: string;
  schema: string;
  problem: string;
};

type Review = { result: DiffResult; desired: string; schema: string };
const labels: Record<DiffChangeKind, string> = {
  add_table: "Tambah tabel", drop_table: "Hapus tabel", alter_table: "Ubah tabel",
  add_column: "Tambah kolom", drop_column: "Hapus kolom", alter_column: "Ubah kolom",
  add_index: "Tambah index", drop_index: "Hapus index", alter_index: "Ubah index",
  add_constraint: "Tambah constraint", drop_constraint: "Hapus constraint", alter_constraint: "Ubah constraint",
  other: "Perubahan lain",
};
const ignoreSelection = () => {};
const navigateDocument = (url: string) => location.assign(url);
const confirmReplace = () => window.confirm("Perbandingan ulang akan mengganti SQL kandidat yang belum disimpan. Lanjutkan?");

export function SchemaDiffPage({ initial, navigate = navigateDocument, confirmDiscard = confirmReplace }: {
  initial: DiffInitialState;
  navigate?: (url: string) => void;
  confirmDiscard?: () => boolean;
}) {
  const db = initial.database;
  const base = `/databases/${encodeURIComponent(db.id)}`;
  const [desired, setDesired] = useState(initial.desired_database_id);
  const [schema, setSchema] = useState(initial.schema);
  const [review, setReview] = useState<Review | null>(null);
  const [sql, setSQL] = useState("");
  const [title, setTitle] = useState("");
  const [failure, setFailure] = useState<unknown>(null);
  const [validation, setValidation] = useState("");
  const [busy, setBusy] = useState<"generate" | "save" | null>(null);
  const [saved, setSaved] = useState<MigrationDraft | null>(null);
  const inflight = useRef<AbortController | null>(null);
  const canGenerate = db.effective_access === "edit" && db.capabilities.schema_diff && db.capabilities.transactional_migrations && !initial.problem && desired !== "" && schema !== "";
  const matches = review?.desired === desired && review.schema === schema;
  const locked = busy !== null || saved !== null;
  const dirty = review !== null && sql !== review.result.candidate_sql;
  const allowNavigation = useUnsavedChanges(!saved && Boolean(review?.result.executable && review.result.candidate_sql.trim()));

  useEffect(() => () => inflight.current?.abort(), []);

  async function act(kind: "generate" | "save", fn: (signal: AbortSignal) => Promise<void>) {
    if (inflight.current || saved) return;
    const controller = new AbortController();
    inflight.current = controller;
    setBusy(kind); setFailure(null); setValidation("");
    try { await fn(controller.signal); }
    catch (err) { if (!isAbortError(err)) setFailure(err); }
    finally {
      if (inflight.current === controller) { inflight.current = null; setBusy(null); }
    }
  }

  function generate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canGenerate || inflight.current || saved || dirty && !confirmDiscard()) return;
    void act("generate", async (signal) => {
      const response = await request<DiffResult>(`${base}/schema-diff`, {
        method: "POST", body: { desired_database_id: desired, schema }, signal,
      });
      if (signal.aborted) return;
      setReview({ result: response!.data, desired, schema });
      setSQL(response!.data.candidate_sql);
      if (!title.trim()) {
        const name = initial.comparisons.find((other) => other.id === desired)?.name ?? "pembanding";
        setTitle(`Perubahan ${schema} dari ${name}`.slice(0, 200));
      }
    });
  }

  function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!review || !matches || !review.result.executable || !canGenerate || inflight.current || saved) return;
    if (!title.trim() || !sql.trim() || sql.includes("\0") || new TextEncoder().encode(sql).byteLength > (256 << 10)) {
      setValidation("Judul dan SQL wajib diisi. SQL maksimal 256 KiB dan tidak boleh mengandung NUL.");
      return;
    }
    void act("save", async (signal) => {
      const response = await request<MigrationDraft>(`${base}/migration-drafts`, {
        method: "POST", signal,
        body: { title, sql, expected_version: review.result.expected_version, source_fingerprint: review.result.source_fingerprint },
      });
      if (signal.aborted) return;
      setSaved(response!.data);
      allowNavigation();
      navigate(`${base}/migrations?draft=${encodeURIComponent(response!.data.id)}#migration-draft`);
    });
  }

  return <section className="schema-diff" aria-busy={busy !== null}>
    <h2>Perbandingan schema: {db.name}</h2>
    <p>Source {db.driver}. Bandingkan dengan schema yang diinginkan, tinjau kandidat SQL, lalu simpan sebagai draft.</p>
    <nav className="migration-actions"><a href={base}>Lihat schema dan data</a><a href={`${base}/migrations`}>Migrasi dan riwayat</a></nav>
    {initial.problem && <p role="alert">{initial.problem}</p>}
    {initial.comparisons.length === 0 && <p>Belum ada database pembanding sejenis yang dapat Anda lihat.</p>}
    {!initial.problem && initial.schemas.length === 0 && <p>Tidak ada schema pengguna yang dapat dibandingkan.</p>}
    <form onSubmit={generate}>
      <fieldset disabled={locked} className="diff-options">
        <label>Database pembanding <select value={desired} onChange={(event) => setDesired(event.target.value)}>
          {initial.comparisons.map((other) => <option key={other.id} value={other.id}>{other.name} ({other.driver})</option>)}
        </select></label>
        <label>Schema <select value={schema} onChange={(event) => setSchema(event.target.value)}>
          {initial.schemas.map((name) => <option key={name} value={name}>{name}</option>)}
        </select></label>
        <button type="submit" disabled={!canGenerate}>Bandingkan schema</button>
      </fieldset>
    </form>
    <p>Perbandingan tidak mengubah target atau membuat draft secara otomatis.</p>
    {busy && <p role="status">{busy === "generate" ? "Membandingkan schema..." : "Menyimpan draft..."}</p>}
    {failure !== null && <ErrorState error={failure} />}
    {failure instanceof ApiRequestError && failure.status === 401 && <p>Sesi berakhir. SQL Anda tetap ada. <a href={loginUrl()} target="_blank" rel="noopener">Masuk lagi di tab baru</a>, lalu kirim kembali secara manual.</p>}
    {validation && <p role="alert">{validation}</p>}
    {review && <section aria-label="Hasil perbandingan">
      <h3>Hasil perbandingan</h3>
      <p>Schema {review.schema}; pembanding {initial.comparisons.find((other) => other.id === review.desired)?.name}.</p>
      <p>Versi source saat dibandingkan: {review.result.expected_version}</p>
      <details><summary>Fingerprint perbandingan</summary><p>Source <code>{review.result.source_fingerprint}</code></p><p>Pembanding <code>{review.result.desired_fingerprint}</code></p></details>
      {!matches && <p role="alert">Pilihan berubah. Bandingkan ulang sebelum menyimpan. SQL sebelumnya tetap ada di halaman.</p>}
      {review.result.warnings.length > 0 && <div role="alert"><h4>Perlu ditinjau</h4><ul>{review.result.warnings.map((warning, i) => <li key={i}>{warning}</li>)}</ul></div>}
      {review.result.changes.length > 0 ? <div className="migration-table"><table>
        <caption>Perubahan schema</caption>
        <thead><tr><th>Perubahan</th><th>Objek</th><th>Destruktif</th></tr></thead>
        <tbody>{review.result.changes.map((change, i) => <tr key={i}><td>{labels[change.kind] ?? change.kind}</td><td>{change.object}</td><td>{change.destructive ? "Ya, tinjau dampak pada data" : "Tidak"}</td></tr>)}</tbody>
      </table></div> : review.result.executable && <p>Schema identik. Tidak ada SQL untuk disimpan.</p>}
      {!review.result.executable && <p>Kandidat tidak dapat dibuat dengan cakupan yang didukung. <a href={`${base}/migrations`}>Tulis migrasi manual</a>.</p>}
      {review.result.executable && review.result.candidate_sql.trim() !== "" && <form onSubmit={save} className="diff-editor">
        <h3>Kandidat SQL</h3>
        <label>Judul draft <input value={title} maxLength={200} required disabled={locked} onChange={(event) => setTitle(event.target.value)} /></label>
        <p id="migration-sql-help">Tinjau dan edit SQL sebelum menyimpan. Tab memberi dua spasi; Escape lalu Tab memindahkan fokus.</p>
        <SQLEditor value={sql} driver={db.driver} readOnly={locked} onChange={setSQL} onSelection={ignoreSelection} />
        <p>Draft menyimpan versi dan fingerprint hasil perbandingan ini. Runner akan memeriksanya lagi saat Anda menjalankan draft.</p>
        <button type="submit" disabled={locked || !matches || !canGenerate || !sql.trim()}>Simpan sebagai draft</button>
      </form>}
    </section>}
    {saved && <p role="status">Draft tersimpan. <a href={`${base}/migrations?draft=${encodeURIComponent(saved.id)}#migration-draft`}>Buka draft</a>.</p>}
  </section>;
}
