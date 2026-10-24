import { useCallback, useEffect, useRef, useState } from "react";
import { ApiRequestError, isAbortError, request } from "../../api/client";
import type { ApiResponse, DatabaseSummary, MigrationDraft, MigrationHead, MigrationRun } from "../../api/types";
import { useApi, type Query } from "../../api/useApi";
import { EmptyState, ErrorState, Loading } from "../../components/StatusViews";
import { ExplainPanel } from "../explain/ExplainPanel";
import { SQLEditor } from "./SQLEditor";
import "./migrations.css";

const pending = (run: MigrationRun) => run.status === "running" || run.status === "outcome_unknown";
const statusLabel = { running: "Berjalan", succeeded: "Berhasil", failed: "Gagal", outcome_unknown: "Hasil belum pasti" } as const;
const date = (value: string | null) => value ? new Date(value).toLocaleString("id-ID") : "belum tersedia";

export type MigrationInitialState = {
  database: DatabaseSummary;
  drafts: ApiResponse<MigrationDraft[]> | null;
  history: ApiResponse<MigrationRun[]>;
  head: MigrationHead | null;
  head_error: string;
  draft: MigrationDraft | null;
  run: MigrationRun | null;
  draft_offset: number;
  history_offset: number;
};

function loaded<T>(response: ApiResponse<T> | null | undefined): Query<T> | undefined {
  return response ? { status: "success", response } : undefined;
}

export function MigrationsPage({ databaseId, initial }: { databaseId: string; initial?: MigrationInitialState }) {
  return <DatabaseMigrations key={databaseId} id={databaseId} initial={initial?.database.id === databaseId ? initial : undefined} />;
}

function DatabaseMigrations({ id, initial }: { id: string; initial?: MigrationInitialState }) {
  const [summary] = useApi<DatabaseSummary>(`/databases/${encodeURIComponent(id)}`, loaded(initial && { data: initial.database }));
  if (summary.status === "loading") return <Loading />;
  if (summary.status === "error") return <ErrorState error={summary.error} />;
  return <Workbench db={summary.response.data} initial={initial} />;
}

function Workbench({ db, initial }: { db: DatabaseSummary; initial?: MigrationInitialState }) {
  const base = `/databases/${encodeURIComponent(db.id)}`;
  const canEdit = db.effective_access === "edit";
  const [draftOffset, setDraftOffset] = useState(initial?.draft_offset ?? 0);
  const [historyOffset, setHistoryOffset] = useState(initial?.history_offset ?? 0);
  const [drafts, reloadDrafts] = useApi<MigrationDraft[]>(canEdit ? `${base}/migration-drafts?limit=50&offset=${draftOffset}` : null, loaded(initial?.drafts));
  const [history, reloadHistory] = useApi<MigrationRun[]>(`${base}/migration-runs?limit=50&offset=${historyOffset}`, loaded(initial?.history));
  const initialHead: Query<MigrationHead> | undefined = initial?.head ? loaded({ data: initial.head }) : initial?.head_error ? {
    status: "error", error: new ApiRequestError({ code: "TARGET_UNAVAILABLE", message: initial.head_error, fields: {}, request_id: "" }, 502),
  } : undefined;
  const [head, reloadHead] = useApi<MigrationHead>(`${base}/migration-head`, initialHead);
  const [draft, setDraft] = useState<MigrationDraft | null>(initial?.draft ?? null);
  const [title, setTitle] = useState(initial?.draft?.title ?? "");
  const [sql, setSQL] = useState(initial?.draft?.sql ?? "");
  const [explainSQL, setExplainSQL] = useState(initial?.draft?.sql ?? "");
  const [version, setVersion] = useState(initial?.draft?.expected_version ?? initial?.head?.version ?? 0);
  const [run, setRun] = useState<MigrationRun | null>(initial?.run ?? null);
  const [failure, setFailure] = useState<unknown>(null);
  const [notice, setNotice] = useState("");
  const [busy, setBusy] = useState(false);
  const inflight = useRef<AbortController | null>(null);
  const key = useRef<{ combination: string; value: string; failed: boolean; runId?: string } | null>(null);
  const currentDraft = useRef(draft);
  currentDraft.current = draft;
  const dirty = draft ? title !== draft.title || sql !== draft.sql || version !== draft.expected_version : true;

  useEffect(() => () => inflight.current?.abort(), []);
  useEffect(() => {
    if (!draft && sql === "" && head.status === "success") setVersion(head.response.data.version);
  }, [draft, sql, head]);

  function showDraft(next: MigrationDraft | null) {
    setDraft(next);
    setTitle(next?.title ?? "");
    setSQL(next?.sql ?? "");
    setExplainSQL(next?.sql ?? "");
    setVersion(next?.expected_version ?? (head.status === "success" ? head.response.data.version : 0));
    setFailure(null);
    setNotice("");
  }

  const refresh = useCallback(() => {
    setHistoryOffset(0);
    reloadDrafts(); reloadHistory(); reloadHead();
  }, [reloadDrafts, reloadHistory, reloadHead]);

  const onTerminal = useCallback(async (result: MigrationRun) => {
    if (key.current?.runId === result.id) key.current.failed = result.status === "failed";
    refresh();
    if (result.status === "succeeded" && currentDraft.current?.id === result.draft_id) {
      setDraft((old) => old?.id === result.draft_id ? { ...old, applied_at: result.finished_at } : old);
    }
  }, [refresh]);

  async function act(fn: (signal: AbortSignal) => Promise<void>) {
    if (inflight.current) return;
    const controller = new AbortController();
    inflight.current = controller;
    setBusy(true); setFailure(null); setNotice("");
    try { await fn(controller.signal); }
    catch (err) { if (!isAbortError(err)) setFailure(err); }
    finally {
      if (inflight.current === controller) { inflight.current = null; setBusy(false); }
    }
  }

  function save() {
    void act(async (signal) => {
      const result = await request<MigrationDraft>(`${base}/migration-drafts${draft ? `/${draft.id}` : ""}`, {
        method: draft ? "PATCH" : "POST",
        body: { title, sql, expected_version: version, ...(draft ? { revision: draft.revision } : {}) }, signal,
      });
      if (signal.aborted) return;
      showDraft(result!.data); setNotice("Draft tersimpan."); reloadDrafts();
      setDraftOffset(0);
    });
  }

  function execute() {
    if (!draft || dirty) return;
    const saved = draft;
    void act(async (signal) => {
      const combination = `${saved.id}:${saved.revision}:${saved.expected_version}`;
      if (key.current?.combination !== combination || key.current.failed) {
        key.current = { combination, value: crypto.randomUUID(), failed: false };
      }
      try {
        const result = await request<MigrationRun>(`${base}/migration-drafts/${saved.id}/execute`, {
          method: "POST", body: { revision: saved.revision, expected_version: saved.expected_version },
          headers: { "Idempotency-Key": key.current!.value }, signal,
        });
        if (signal.aborted) return;
        key.current!.runId = result!.data.id;
        setRun(result!.data);
        if (!pending(result!.data)) await onTerminal(result!.data);
        else refresh();
      } catch (err) {
        if (err instanceof ApiRequestError && err.run_id) {
          key.current!.runId = err.run_id;
          const detail = await request<MigrationRun>(`${base}/migration-runs/${encodeURIComponent(err.run_id)}`, { signal });
          if (!signal.aborted && detail) {
            setRun(detail.data);
            if (!pending(detail.data)) await onTerminal(detail.data);
            else refresh();
          }
        }
        throw err;
      }
    });
  }

  const blocked = busy || Boolean(draft?.applied_at) || Boolean(run && pending(run));

  return (
    <section className="migrations">
      <h2>Migrasi: {db.name}</h2>
      <p>{db.driver} · akses {canEdit ? "lihat dan edit" : "lihat"}</p>
      <a href={`/databases/${db.id}`}>Lihat schema dan data</a>
      {canEdit && db.capabilities.schema_diff && <p><a href={`/databases/${db.id}/schema-diff`}>Bandingkan schema</a></p>}
      {head.status === "success" && <p>Versi committed: {head.response.data.version}</p>}
      {head.status === "error" && <ErrorState error={head.error} />}
      {notice && <p role="status">{notice}</p>}
      {failure !== null && <ErrorState error={failure} />}
      {canEdit ? (
        <div className="migration-workspace">
          <aside aria-label="Daftar draft">
            <h2>Draft SQL</h2>
            <button type="button" disabled={busy} onClick={() => showDraft(null)}>Draft baru</button>
            {drafts.status === "loading" && <Loading label="Memuat draft..." />}
            {drafts.status === "error" && <ErrorState error={drafts.error} />}
            {drafts.status === "success" && (drafts.response.data.length === 0 ? <EmptyState message="Belum ada draft." /> : (
              <ul>{drafts.response.data.map((item) => (
                <li key={item.id}><button type="button" disabled={busy} aria-pressed={draft?.id === item.id} onClick={() => {
                  void act(async (signal) => {
                    const result = await request<MigrationDraft>(`${base}/migration-drafts/${item.id}`, { signal });
                    if (!signal.aborted) showDraft(result!.data);
                  });
                }}>{item.title}{item.applied_at ? " (diterapkan)" : ""}</button></li>
              ))}</ul>
            ))}
            {drafts.status === "success" && <nav aria-label="Halaman draft" className="migration-actions">
              <button type="button" disabled={busy || draftOffset === 0} onClick={() => setDraftOffset((offset) => Math.max(0, offset - 50))}>Draft sebelumnya</button>
              <button type="button" disabled={busy || !drafts.response.page?.has_more} onClick={() => setDraftOffset((offset) => offset + 50)}>Draft berikutnya</button>
            </nav>}
          </aside>
          <div className="migration-editor">
            <h2>{draft ? `Draft revision ${draft.revision}` : "Draft baru"}</h2>
            <label>Judul <input value={title} maxLength={200} disabled={blocked} onChange={(e) => setTitle(e.target.value)} /></label>
            <label>Versi yang diharapkan <input type="number" min={0} max={Number.MAX_SAFE_INTEGER - 1} step={1} value={version} disabled={blocked} onChange={(e) => setVersion(Number(e.target.value))} /></label>
            {head.status === "success" && version !== head.response.data.version && <button type="button" disabled={blocked} onClick={() => { if (head.status === "success") setVersion(head.response.data.version); }}>Gunakan versi terkini</button>}
            <p id="migration-sql-help">SQL dijalankan dalam satu transaksi. Pilih teks untuk EXPLAIN; tanpa seleksi, seluruh isi editor dikirim. Tab menambah dua spasi, Shift+Tab mengurangi indentasi. Tekan Escape lalu Tab atau Shift+Tab untuk berpindah fokus.</p>
            <SQLEditor value={sql} driver={db.driver} readOnly={blocked} onChange={setSQL} onSelection={setExplainSQL} />
            <div className="migration-actions">
              <button type="button" onClick={save} disabled={blocked || !dirty || !title.trim() || !sql.trim() || !Number.isSafeInteger(version) || version < 0}>Simpan draft</button>
              <button type="button" onClick={execute} disabled={blocked || !draft || dirty || !db.capabilities.transactional_migrations}>Jalankan migrasi</button>
            </div>
            {draft?.applied_at && <p>Draft sudah diterapkan. Buat draft baru untuk perubahan berikutnya.</p>}
            {!db.capabilities.transactional_migrations && <p>Eksekusi belum tersedia. Admin perlu menguji koneksi dan menyiapkan ledger.</p>}
            {draft && dirty && <p>Simpan perubahan sebelum menjalankan migrasi.</p>}
            <ExplainPanel databaseId={db.id} sql={explainSQL} canExplain={canEdit && db.capabilities.explain} />
          </div>
        </div>
      ) : <p>Akses lihat hanya dapat membaca riwayat migrasi.</p>}
      <h2>Riwayat migrasi</h2>
      {history.status === "loading" && <Loading label="Memuat riwayat..." />}
      {history.status === "error" && <ErrorState error={history.error} />}
      {history.status === "success" && (history.response.data.length === 0 ? <EmptyState message="Belum ada eksekusi migrasi." /> : (
        <div className="migration-table"><table>
          <thead><tr><th>Status</th><th>Versi percobaan</th><th>Versi committed</th><th>Pelaksana</th><th>Mulai</th><th>Detail</th></tr></thead>
          <tbody>{history.response.data.map((item) => <tr key={item.id}>
            <td>{statusLabel[item.status]}</td><td>{item.candidate_version}</td><td>{item.version ?? "belum committed"}</td><td>{item.actor.username}</td><td>{date(item.started_at)}</td>
            <td><button type="button" onClick={() => setRun(item)}>Lihat SQL dan status</button></td>
          </tr>)}</tbody>
        </table></div>
      ))}
      {history.status === "success" && <nav aria-label="Halaman riwayat" className="migration-actions">
        <button type="button" disabled={historyOffset === 0} onClick={() => setHistoryOffset((offset) => Math.max(0, offset - 50))}>Riwayat sebelumnya</button>
        <button type="button" disabled={!history.response.page?.has_more} onClick={() => setHistoryOffset((offset) => offset + 50)}>Riwayat berikutnya</button>
      </nav>}
      {run && <RunDetail key={run.id} base={base} run={run} setRun={setRun} canEdit={canEdit} onTerminal={onTerminal} />}
    </section>
  );
}

export function RunDetail({ base, run, setRun, canEdit, onTerminal }: {
  base: string; run: MigrationRun; setRun: (run: MigrationRun) => void; canEdit: boolean; onTerminal: (run: MigrationRun) => Promise<void>;
}) {
  const [error, setError] = useState<unknown>(null);
  const [stopped, setStopped] = useState(false);
  const [busy, setBusy] = useState(false);
  const [refresh, setRefresh] = useState(0);
  const attempts = useRef(0);
  const callback = useRef(onTerminal);
  callback.current = onTerminal;

  useEffect(() => {
    if (!pending(run)) return;
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout>;
    const poll = async () => {
      if (attempts.current >= 30) { setStopped(true); return; }
      attempts.current++;
      try {
        const result = await request<MigrationRun>(`${base}/migration-runs/${run.id}`, { signal: controller.signal });
        if (controller.signal.aborted) return;
        setRun(result!.data);
        if (!pending(result!.data)) { await callback.current(result!.data); return; }
      } catch (err) {
        if (isAbortError(err)) return;
        setError(err); setStopped(true); return;
      }
      timer = setTimeout(poll, 2000);
    };
    timer = setTimeout(poll, 2000);
    return () => { controller.abort(); clearTimeout(timer); };
  }, [base, run.id, run.status, setRun, refresh]);

  async function reconcile() {
    if (busy) return;
    setBusy(true); setError(null);
    try {
      const result = await request<MigrationRun>(`${base}/migration-runs/${run.id}/reconcile`, { method: "POST", body: {} });
      setRun(result!.data);
      if (!pending(result!.data)) await onTerminal(result!.data);
    } catch (err) { setError(err); }
    finally { setBusy(false); }
  }

  return <section aria-label="Detail eksekusi">
    <h2>Detail eksekusi</h2>
    <p>Status: {statusLabel[run.status]}</p>
    <p>Versi percobaan: {run.candidate_version}; versi committed: {run.version ?? "belum committed"}</p>
    <p>Pelaksana: {run.actor.username}; mulai: {date(run.started_at)}; selesai: {date(run.finished_at)}</p>
    {run.error && <p role="alert">{run.error.code}: {run.error.message}</p>}
    <pre>{run.sql}</pre>
    {pending(run) && <p>Hasil belum definitif. SQL tidak dijalankan ulang saat memeriksa status.</p>}
    {stopped && <p>Pemeriksaan otomatis berhenti. Perbarui status atau rekonsiliasi secara manual.</p>}
    {error !== null && <ErrorState error={error} />}
    {pending(run) && <div className="migration-actions">
      <button type="button" onClick={() => { attempts.current = 0; setStopped(false); setError(null); setRefresh((v) => v + 1); }}>Perbarui status</button>
      {canEdit && <button type="button" disabled={busy} onClick={() => void reconcile()}>Rekonsiliasi</button>}
    </div>}
  </section>;
}
