import { useEffect, useRef, useState, type FormEvent } from "react";
import { ApiRequestError, isAbortError, request } from "../../api/client";
import type { DatabaseSummary, DiffChangeKind, DiffResult } from "../../api/types";
import { loginUrl } from "../../app/mount";
import { ErrorState } from "../../components/StatusViews";
import { SQLEditor } from "../migrations/SQLEditor";
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
const ignore = () => {};

export function SchemaDiffPage({ initial }: { initial: DiffInitialState }) {
  const db = initial.database;
  const base = `/databases/${encodeURIComponent(db.id)}`;
  const [desired, setDesired] = useState(initial.desired_database_id);
  const [schema, setSchema] = useState(initial.schema);
  const [review, setReview] = useState<Review | null>(null);
  const [failure, setFailure] = useState<unknown>(null);
  const [busy, setBusy] = useState(false);
  const inflight = useRef<AbortController | null>(null);
  const canGenerate = db.effective_access === "edit" && db.capabilities.schema_diff && db.capabilities.transactional_migrations && !initial.problem && desired !== "" && schema !== "";
  const matches = review?.desired === desired && review.schema === schema;

  useEffect(() => () => inflight.current?.abort(), []);

  function generate(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!canGenerate || inflight.current) return;
    const controller = new AbortController();
    inflight.current = controller;
    setBusy(true); setFailure(null);
    void (async () => {
      try {
        const response = await request<DiffResult>(`${base}/schema-diff`, {
          method: "POST", body: { desired_database_id: desired, schema }, signal: controller.signal,
        });
        if (!controller.signal.aborted) setReview({ result: response!.data, desired, schema });
      } catch (err) {
        if (!isAbortError(err)) setFailure(err);
      } finally {
        if (inflight.current === controller) { inflight.current = null; setBusy(false); }
      }
    })();
  }

  return <section className="schema-diff" aria-busy={busy}>
    <h2>Perbandingan schema: {db.name}</h2>
    <p>Source {db.driver}. Bandingkan dengan schema yang diinginkan, lalu tinjau kandidat SQL.</p>
    <nav className="migration-actions" aria-label="Halaman database"><a className="button-link button-link-secondary" href={base}>Lihat schema dan data</a></nav>
    {initial.problem && <p role="alert">{initial.problem}</p>}
    {initial.comparisons.length === 0 && <p className="empty-state">Belum ada database pembanding sejenis yang dapat Anda lihat.</p>}
    {!initial.problem && initial.schemas.length === 0 && <p>Tidak ada schema pengguna yang dapat dibandingkan.</p>}
    <form onSubmit={generate}>
      <fieldset disabled={busy} className="diff-options">
        <label>Database pembanding <select value={desired} onChange={(event) => setDesired(event.target.value)}>
          {initial.comparisons.map((other) => <option key={other.id} value={other.id}>{other.name} ({other.driver})</option>)}
        </select></label>
        <label>Schema <select value={schema} onChange={(event) => setSchema(event.target.value)}>
          {initial.schemas.map((name) => <option key={name} value={name}>{name}</option>)}
        </select></label>
        <button type="submit" disabled={!canGenerate}>Bandingkan schema</button>
      </fieldset>
    </form>
    <p>Perbandingan tidak mengubah target.</p>
    {busy && <p role="status" className="loading">Membandingkan schema...</p>}
    {failure !== null && <ErrorState error={failure} />}
    {failure instanceof ApiRequestError && failure.status === 401 && <p>Sesi berakhir. <a href={loginUrl()} target="_blank" rel="noopener">Masuk lagi di tab baru</a>, lalu bandingkan ulang.</p>}
    {review && <section aria-label="Hasil perbandingan">
      <h3>Hasil perbandingan</h3>
      <p>Schema {review.schema}; pembanding {initial.comparisons.find((other) => other.id === review.desired)?.name}.</p>
      <p>Versi source saat dibandingkan: {review.result.expected_version}</p>
      <details><summary>Fingerprint perbandingan</summary><p>Source <code>{review.result.source_fingerprint}</code></p><p>Pembanding <code>{review.result.desired_fingerprint}</code></p></details>
      {!matches && <p role="alert">Pilihan berubah. Bandingkan ulang untuk melihat hasil terbaru.</p>}
      {review.result.warnings.length > 0 && <div role="alert" className="alert alert-warning"><h4>Perlu ditinjau</h4><ul>{review.result.warnings.map((warning, i) => <li key={i}>{warning}</li>)}</ul></div>}
      {review.result.changes.length > 0 ? <div className="migration-table"><table>
        <caption>Perubahan schema</caption>
        <thead><tr><th>Perubahan</th><th>Objek</th><th>Destruktif</th></tr></thead>
        <tbody>{review.result.changes.map((change, i) => <tr key={i}><td>{labels[change.kind] ?? change.kind}</td><td>{change.object}</td><td>{change.destructive ? <span className="status-badge" data-status="failed">Ya, tinjau dampak pada data</span> : "Tidak"}</td></tr>)}</tbody>
      </table></div> : review.result.executable && <p>Schema identik. Tidak ada SQL kandidat.</p>}
      {!review.result.executable && <p>Kandidat tidak dapat dibuat dengan cakupan yang didukung.</p>}
      {review.result.executable && review.result.candidate_sql.trim() !== "" && <div className="diff-editor">
        <h3>Kandidat SQL</h3>
        <SQLEditor value={review.result.candidate_sql} driver={db.driver} readOnly onChange={ignore} onSelection={ignore} />
      </div>}
    </section>}
  </section>;
}
