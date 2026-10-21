import { Link } from "react-router-dom";
import type { DatabaseSummary } from "../../api/types";
import { useApi } from "../../api/useApi";
import { EmptyState, ErrorState, Loading } from "../../components/StatusViews";

const accessLabel = { none: "tanpa akses", view: "lihat", edit: "lihat dan edit" } as const;

export function DatabaseList() {
  const [q] = useApi<DatabaseSummary[]>("/databases?limit=200");
  if (q.status === "loading") return <Loading />;
  if (q.status === "error") return <ErrorState error={q.error} />;
  const dbs = q.response.data;
  return (
    <section>
      <h1>Database</h1>
      {dbs.length === 0 ? (
        <EmptyState message="Belum ada database yang dapat Anda akses." />
      ) : (
        <ul>
          {dbs.map((d) => (
            <li key={d.id}>
              <Link to={`/databases/${d.id}`}>{d.name}</Link> ({d.driver}, akses {accessLabel[d.effective_access]})
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}
