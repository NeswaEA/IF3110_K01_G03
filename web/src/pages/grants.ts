import "../app/layout.css";
import "../features/access/admin.css";
import { bindGrants } from "../features/access/grants";

const root = document.querySelector<HTMLElement>(".grants-admin");
if (root) bindGrants(root, (url) => location.assign(url));
