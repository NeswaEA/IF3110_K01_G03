import type { ReactNode } from "react";
import { Table, TableBody, TableCaption, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";

export type Column<T> = { id: string; header: ReactNode; cell: (row: T) => ReactNode; className?: string };

/**
 * A table that scrolls inside its own box, so wide or long data never makes
 * the page scroll sideways.
 */
export function DataTable<T>({ columns, rows, rowKey, caption, maxHeight = "70vh" }: {
  columns: Column<T>[];
  rows: T[];
  rowKey: (row: T, index: number) => string;
  caption: string;
  maxHeight?: string;
}) {
  return (
    <div className="max-w-full overflow-auto rounded-md border" style={{ maxHeight }} tabIndex={0} role="region" aria-label={caption}>
      <Table>
        <TableCaption className="sr-only">{caption}</TableCaption>
        <TableHeader className="sticky top-0 z-10 bg-background">
          <TableRow>
            {columns.map((c) => (
              <TableHead key={c.id} className={c.className} scope="col">
                {c.header}
              </TableHead>
            ))}
          </TableRow>
        </TableHeader>
        <TableBody>
          {rows.map((row, i) => (
            <TableRow key={rowKey(row, i)}>
              {columns.map((c) => (
                <TableCell key={c.id} className={c.className}>
                  {c.cell(row)}
                </TableCell>
              ))}
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  );
}
