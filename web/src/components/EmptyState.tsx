import { Inbox, type LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

export function EmptyState({ title, description, icon: Icon = Inbox, action }: {
  title: string;
  description?: ReactNode;
  icon?: LucideIcon;
  action?: ReactNode;
}) {
  return (
    <div role="status" className="flex flex-col items-center gap-2 rounded-lg border border-dashed p-8 text-center">
      <Icon aria-hidden className="size-8 text-muted-foreground" />
      <p className="font-medium">{title}</p>
      {description && <p className="max-w-prose text-sm text-muted-foreground">{description}</p>}
      {action}
    </div>
  );
}
