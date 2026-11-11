import type { FormEvent } from "react";
import { Input } from "@/components/ui/input";
import { Field, FormFooter, type Submit, useSubmit } from "./form";

export function GroupNameForm({ current, onSubmit, onCancel }: { current?: string; onSubmit: Submit<string>; onCancel: () => void }) {
  const { error, pending, run } = useSubmit(onSubmit);
  function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    void run(String(new FormData(e.currentTarget).get("name") ?? "").trim());
  }
  return (
    <form onSubmit={submit} className="grid gap-4">
      <Field id="group-name" label="Nama grup" hint="1-100 karakter, unik." error={error?.fields.name}>
        <Input id="group-name" aria-describedby="group-name-help" name="name" required maxLength={100} defaultValue={current} aria-invalid={!!error?.fields.name} />
      </Field>
      <FormFooter error={error} pending={pending} onCancel={onCancel} submitLabel="Simpan" />
    </form>
  );
}
