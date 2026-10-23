import "../app/layout.css";
import "../features/access/admin.css";
import { bindGroups } from "../features/access/groups";

const root = document.querySelector<HTMLElement>(".groups-admin");
if (root) bindGroups(root, (url) => location.assign(url));
