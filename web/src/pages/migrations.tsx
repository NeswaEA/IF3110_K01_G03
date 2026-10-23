import { mountWidget } from "../app/mount";
import { MigrationsPage } from "../features/migrations/MigrationsPage";

// The editor must not lose unsent SQL when the session expires, so it is notified instead of redirected.
mountWidget("migrations", (el) => <MigrationsPage databaseId={el.dataset.databaseId ?? ""} />, "notify");
