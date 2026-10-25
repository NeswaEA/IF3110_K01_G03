import "../app/layout.css";
import "../features/access/admin.css";
import { enhancePasswords } from "../components/passwordToggle";
import { bindUsers } from "../features/access/users";

const root = document.querySelector<HTMLElement>(".users-admin");
if (root) {
  enhancePasswords(root);
  bindUsers(root, (url) => location.assign(url));
}
