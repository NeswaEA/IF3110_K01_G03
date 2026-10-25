import "../app/layout.css";
import { bindLogout } from "../app/logout";

const logout = document.querySelector<HTMLButtonElement>('[data-action="logout"]');
if (logout) bindLogout(logout, (url) => location.assign(url));

const navigation = document.querySelector<HTMLDetailsElement>(".mobile-navigation");
navigation?.addEventListener("keydown", (event) => {
  if (event.key === "Escape") {
    navigation.open = false;
    navigation.querySelector("summary")?.focus();
  }
});
