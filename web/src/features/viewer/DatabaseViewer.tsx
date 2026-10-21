import { useState } from "react";
import { useParams } from "react-router-dom";
import type { DataColumn, DatabaseSummary, JSONValue, SchemaSnapshot, Table, TableData } from "../../api/types";
import { useApi } from "../../api/useApi";
import { EmptyState, ErrorState, Loading } from "../../components/StatusViews";

const PAGE = 50;

function Cell({ value, column }: { value: JSONValue; column: DataColumn }) {
  if (value === null) return <em>NULL</em>;
  if (column.encoding === "base64") return <span title="Data biner (base64)">{String(value)}</span>;
  if (typeof value === "object") return <code>{JSON.stringify(value)}</code>; // rendered as text, never as HTML
  return <>{String(value)}</>;
}

function TableRows({ databaseId, schema, table }: { databaseId: string; schema: string; table: string }) {
  const [offset, setOffset] = useState(0);
  const params = new URLSearchParams({ schema, table, limit: String(PAGE), offset: String(offset) });
  const [q] = useApi<TableData>(`/databases/${encodeURIComponent(databaseId)}/data?${params}`);
  if (q.status === "loading") return <Loading label="Memuat data..." />;
  if (q.status === "error") return <ErrorState error={q.error} />;
  const { data, page } = q.response;
  return (
    <div>
      {!data.order_stable && (
        <p>Tabel ini tidak memiliki primary key, sehingga urutan baris antarhalaman tidak dijamin.</p>
      )}
      {data.rows.length === 0 ? (
        <EmptyState message="Tabel tidak memiliki baris." />
      ) : (
        <div style={{ overflowX: "auto" }}>
          <table>
            <thead>
              <tr>
                {data.columns.map((c) => (
                  <th key={c.name} scope="col">
                    {c.name} <small>({c.data_type})</small>
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {data.rows.map((row, i) => (
                <tr key={offset + i}>
                  {row.map((v, j) => (
                    <td key={data.columns[j].name}>
                      <Cell value={v} column={data.columns[j]} />
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      <nav aria-label="Halaman data">
        <button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - PAGE))}>
          Sebelumnya
        </button>
        <span>
          Baris {offset + 1}-{offset + data.rows.length}
        </span>
        <button type="button" disabled={!page?.has_more} onClick={() => setOffset(offset + PAGE)}>
          Berikutnya
        </button>
      </nav>
    </div>
  );
}

function TableDetail({ table }: { table: Table }) {
  return (
    <div>
      <h3>Kolom</h3>
      <table>
        <thead>
          <tr>
            <th scope="col">#</th>
            <th scope="col">Nama</th>
            <th scope="col">Tipe</th>
            <th scope="col">Nullable</th>
            <th scope="col">Default</th>
          </tr>
        </thead>
        <tbody>
          {table.columns.map((c) => (
            <tr key={c.name}>
              <td>{c.ordinal}</td>
              <td>{c.name}</td>
              <td>{c.data_type}</td>
              <td>{c.nullable ? "ya" : "tidak"}</td>
              <td>{c.default ?? "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
      <h3>Constraint</h3>
      {table.constraints.length === 0 ? (
        <EmptyState message="Tidak ada constraint." />
      ) : (
        <ul>
          {table.constraints.map((c, i) => (
            <li key={`${c.kind}-${c.name ?? i}`}>
              {c.kind} {c.name ?? "(tanpa nama)"}: <code>{c.definition}</code>
            </li>
          ))}
        </ul>
      )}
      <h3>Indeks</h3>
      {table.indexes.length === 0 ? (
        <EmptyState message="Tidak ada indeks." />
      ) : (
        <ul>
          {table.indexes.map((ix) => (
            <li key={ix.name}>
              {ix.name} ({ix.columns.join(", ") || "ekspresi"}){ix.primary ? ", primary" : ix.unique ? ", unique" : ""}:{" "}
              <code>{ix.definition}</code>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

export function DatabaseViewer() {
  const { id = "" } = useParams();
  const [summary] = useApi<DatabaseSummary>(`/databases/${encodeURIComponent(id)}`);
  const [schema] = useApi<SchemaSnapshot>(`/databases/${encodeURIComponent(id)}/schema`);
  const [selected, setSelected] = useState<{ schema: string; table: string } | null>(null);

  if (summary.status === "error") return <ErrorState error={summary.error} />;
  if (schema.status === "error") return <ErrorState error={schema.error} />;
  if (summary.status === "loading" || schema.status === "loading") return <Loading />;

  const db = summary.response.data;
  const snap = schema.response.data;
  const table = selected
    ? snap.schemas.find((s) => s.name === selected.schema)?.tables.find((t) => t.name === selected.table)
    : undefined;

  return (
    <section>
      <h1>{db.name}</h1>
      <p>
        Engine {db.driver}; fingerprint <code>{snap.fingerprint}</code>
      </p>
      <div style={{ display: "flex", flexWrap: "wrap", gap: "1rem" }}>
        <nav aria-label="Schema" style={{ minWidth: "12rem" }}>
          {snap.schemas.length === 0 ? (
            <EmptyState message="Tidak ada schema pengguna." />
          ) : (
            <ul>
              {snap.schemas.map((s) => (
                <li key={s.name}>
                  {s.name}
                  {s.tables.length === 0 ? (
                    <p>Tidak ada tabel.</p>
                  ) : (
                    <ul>
                      {s.tables.map((t) => (
                        <li key={t.name}>
                          <button
                            type="button"
                            aria-pressed={selected?.schema === s.name && selected.table === t.name}
                            onClick={() => setSelected({ schema: s.name, table: t.name })}
                          >
                            {t.name}
                          </button>
                        </li>
                      ))}
                    </ul>
                  )}
                </li>
              ))}
            </ul>
          )}
        </nav>
        <div style={{ flex: "1 1 20rem", minWidth: 0 }}>
          {table && selected ? (
            <>
              <h2>
                {selected.schema}.{table.name}
              </h2>
              <TableDetail table={table} />
              <h3>Data</h3>
              <TableRows key={`${selected.schema}.${table.name}`} databaseId={db.id} schema={selected.schema} table={table.name} />
            </>
          ) : (
            <p>Pilih tabel untuk melihat struktur dan datanya.</p>
          )}
        </div>
      </div>
    </section>
  );
}
