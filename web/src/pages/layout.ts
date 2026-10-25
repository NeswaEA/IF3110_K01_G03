import "../app/layout.css";
import { bindLogout } from "../app/logout";
import { bindDatabaseFilters } from "../features/viewer/databases";

const logout = document.querySelector<HTMLButtonElement>('[data-action="logout"]');
if (logout) bindLogout(logout, (url) => location.assign(url));

const databases = document.querySelector<HTMLElement>("[data-database-list]");
if (databases) bindDatabaseFilters(databases);

const navigation = document.querySelector<HTMLDetailsElement>(".mobile-navigation");
navigation?.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    navigation.open = false;
    navigation.querySelector("summary")?.focus();
  }
});
