/** Flags the inputs named by an API error's fields and focuses the first one; an empty list clears every flag. */
export function markInvalid(form: HTMLFormElement, names: string[]): void {
  for (const el of form.querySelectorAll("[aria-invalid]")) el.removeAttribute("aria-invalid");
  const invalid = names.map((name) => form.elements.namedItem(name)).filter((el): el is HTMLElement => el instanceof HTMLElement);
  for (const el of invalid) el.setAttribute("aria-invalid", "true");
  invalid[0]?.focus();
}
