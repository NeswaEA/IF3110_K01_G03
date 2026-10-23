import "../app/layout.css";
import "../features/access/admin.css";
import { bindMembers } from "../features/access/members";

const root = document.querySelector<HTMLElement>(".members-admin");
if (root) bindMembers(root, (url) => location.assign(url));
