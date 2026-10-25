// Eye button for password inputs: an open eye reveals the password, a closed eye hides it again.
const icon = (name: string, paths: string) =>
  `<svg class="${name}" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">${paths}</svg>`;

export const PASSWORD_ICONS =
  icon("eye-open", '<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/>') +
  icon("eye-closed", '<path d="M2.5 10s3.5 5 9.5 5 9.5-5 9.5-5"/><path d="m5 13.4-2 2.3M9.2 14.8l-.7 2.9M14.8 14.8l.7 2.9M19 13.4l2 2.3"/>');

export const passwordToggleLabel = (visible: boolean) => visible ? "Sembunyikan password" : "Tampilkan password";

function show(input: HTMLInputElement, button: HTMLButtonElement, visible: boolean) {
  input.type = visible ? "text" : "password";
  button.setAttribute("aria-label", passwordToggleLabel(visible));
  button.dataset.visible = String(visible);
}

/** Adds the button to every input[data-reveal] under root; the password is hidden again on submit. */
export function enhancePasswords(root: ParentNode): void {
  for (const input of root.querySelectorAll<HTMLInputElement>("input[data-reveal]")) {
    const control = document.createElement("span");
    control.className = "password-control";
    const button = document.createElement("button");
    button.type = "button";
    button.className = "password-toggle";
    button.setAttribute("aria-controls", input.id);
    button.innerHTML = PASSWORD_ICONS;
    input.replaceWith(control);
    control.append(input, button);
    show(input, button, false);
    button.addEventListener("click", () => {
      show(input, button, input.type === "password");
      input.focus();
    });
    input.form?.addEventListener("submit", () => show(input, button, false));
  }
}
