import "../app/layout.css";
import "../features/auth/login.css";
import { bindLogin } from "../features/auth/login";

const form = document.querySelector<HTMLFormElement>("#login-form");
if (form) bindLogin(form, (url) => location.assign(url));
