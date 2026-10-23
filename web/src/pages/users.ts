import "../app/layout.css";
import "../features/access/users.css";
import { bindUsers } from "../features/access/users";

const root = document.querySelector<HTMLElement>(".users-admin");
if (root) bindUsers(root, (url) => location.assign(url));
