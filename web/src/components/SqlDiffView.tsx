import type { Schemas } from "@/api/client";

function diffSections(before: string, after: string) {
  const left = before.split("\n");
  const right = after.split("\n");
  const shortest = Math.min(left.length, right.length);
  let prefix = 0;
  while (prefix < shortest && left[prefix] === right[prefix]) prefix++;
  let suffix = 0;
  while (suffix < shortest - prefix && left[left.length - 1 - suffix] === right[right.length - 1 - suffix]) suffix++;
  return [left, right].map((lines) => {
    const end = lines.length - suffix;
    return {
      before: lines.slice(0, prefix).join("\n") + (prefix > 0 && prefix < lines.length ? "\n" : ""),
      changed: lines.slice(prefix, end).join("\n") + (prefix < end && suffix > 0 ? "\n" : ""),
      after: suffix ? lines.slice(end).join("\n") : "",
    };
  });
}

export function SqlDiffView({ before, after }: {before: Schemas["MigrationRevision"]; after: Schemas["MigrationRevision"]}) {
  return <div className="grid min-w-0 gap-4">
    {(["up_sql", "down_sql"] as const).map((field) => {
      const label = field === "up_sql" ? "SQL up" : "SQL down";
      const value = (revision: Schemas["MigrationRevision"]) => revision[field] ?? revision.irreversible_reason ?? "—";
      const sections = diffSections(value(before), value(after));
      return <section key={field} className="grid min-w-0 gap-2">
        <h3 className="font-semibold">{label}</h3>
        <div className="grid min-w-0 gap-2 sm:grid-cols-2">
          {[before, after].map((revision, index) => {
            const parts = sections[index];
            const color = index === 0 ? "bg-red-50 text-red-950" : "bg-emerald-50 text-emerald-950";
            return <div key={index} className="min-w-0 rounded-md border">
              <p className="border-b px-3 py-2 text-xs text-muted-foreground">Revisi {revision.revision}</p>
              <pre aria-label={`${label} revisi ${revision.revision}`} className="max-h-64 overflow-auto p-3 font-mono text-sm">
                {parts.before}{parts.changed && <mark className={color}>{parts.changed}</mark>}{parts.after}
              </pre>
            </div>;
          })}
        </div>
      </section>;
    })}
  </div>;
}
