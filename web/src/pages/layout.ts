import { bindLogout } from "../app/logout";

const logout = document.querySelector<HTMLButtonElement>('[data-action="logout"]');
if (logout) bindLogout(logout, (url) => location.assign(url));
