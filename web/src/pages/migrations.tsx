import { mountWidget } from "../app/mount";
import { MigrationsPage, type MigrationInitialState } from "../features/migrations/MigrationsPage";

// The editor must not lose unsent SQL when the session expires, so it is notified instead of redirected.
mountWidget("migrations", (el) => {
  const initial = el.dataset.initial ? JSON.parse(el.dataset.initial) as MigrationInitialState : undefined;
  return <MigrationsPage databaseId={el.dataset.databaseId ?? ""} initial={initial} />;
}, "notify");
