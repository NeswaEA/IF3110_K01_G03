import { useId, useLayoutEffect, useRef, type KeyboardEvent } from "react";
import type { Schemas } from "@/api/client";

type Driver = Schemas["Driver"];

type Props = {
  label: string;
  value: string;
  driver: Driver;
  readOnly: boolean;
  onChange: (value: string) => void;
  onSelection: (value: string) => void;
};

function selectedSQL(editor: HTMLTextAreaElement) {
  const { value, selectionStart: start, selectionEnd: end } = editor;
  return start === end ? value : value.slice(start, end);
}

function indent(value: string, start: number, end: number, outdent: boolean) {
  if (!outdent && start === end) {
    return { value: value.slice(0, start) + "  " + value.slice(end), start: start + 2, end: end + 2 };
  }
  const first = start === 0 ? 0 : value.lastIndexOf("\n", start - 1) + 1;
  const last = end > start && value[end - 1] === "\n" ? end - 1 : end;
  const lines = [value.slice(0, first)];
  let nextStart = start, nextEnd = end;
  let at = first;
  while (at <= last) {
    const remove = !outdent ? 0 : value[at] === "\t" ? 1 : value[at] === " " ? (value[at + 1] === " " ? 2 : 1) : 0;
    if (outdent) {
      nextStart -= Math.min(remove, Math.max(0, start - at));
      nextEnd -= Math.min(remove, Math.max(0, end - at));
    } else {
      if (at <= start) nextStart += 2;
      if (at < end) nextEnd += 2;
    }
    const newline = value.indexOf("\n", at);
    const next = newline === -1 ? value.length : newline + 1;
    lines.push(outdent ? "" : "  ", value.slice(at + remove, next));
    at = next;
    if (newline === -1) break;
  }
  lines.push(value.slice(at));
  return { value: lines.join(""), start: nextStart, end: nextEnd };
}

export function SqlEditor({ label, value, driver, readOnly, onChange, onSelection }: Props) {
  const help = useId();
  const editor = useRef<HTMLTextAreaElement>(null);
  const releaseTab = useRef(false);
  const selection = useRef<{
    value: string; start: number; end: number; direction: "forward" | "backward" | "none";
    scrollTop: number; scrollLeft: number;
  } | null>(null);

  useLayoutEffect(() => {
    const input = editor.current!;
    const pending = selection.current;
    if (pending?.value === value) {
      input.setSelectionRange(pending.start, pending.end, pending.direction);
      input.scrollTop = pending.scrollTop;
      input.scrollLeft = pending.scrollLeft;
    }
    selection.current = null;
    if (document.activeElement === input) onSelection(selectedSQL(input));
  }, [value, driver, onSelection]);

  function keyDown(event: KeyboardEvent<HTMLTextAreaElement>) {
    if (event.key === "Escape") { releaseTab.current = true; return; }
    if (["Shift", "Control", "Alt", "Meta"].includes(event.key)) return;
    const leave = releaseTab.current;
    releaseTab.current = false;
    if (event.key !== "Tab" || leave || readOnly || event.ctrlKey || event.metaKey || event.altKey || event.nativeEvent.isComposing) return;
    event.preventDefault();
    const input = event.currentTarget;
    const next = indent(value, input.selectionStart, input.selectionEnd, event.shiftKey);
    if (next.value === value) return;
    selection.current = { ...next, direction: input.selectionDirection, scrollTop: input.scrollTop, scrollLeft: input.scrollLeft };
    onChange(next.value);
  }

  return <div className="grid min-w-0 gap-1">
    <span className="text-sm font-medium">{label}</span>
    <textarea
    ref={editor}
    className="h-52 max-h-[50vh] w-full min-w-0 resize-y overflow-auto rounded-md border bg-background p-3 font-mono text-sm focus-visible:outline-ring sm:h-64"
    aria-label={label}
    aria-describedby={readOnly ? undefined : help}
    wrap="off"
    data-driver={driver}
    value={value}
    readOnly={readOnly}
    rows={14}
    spellCheck={false}
    autoComplete="off"
    autoCapitalize="off"
    autoCorrect="off"
    onChange={(event) => {
      if (readOnly) return;
      onChange(event.currentTarget.value);
      onSelection(selectedSQL(event.currentTarget));
    }}
    onSelect={(event) => onSelection(selectedSQL(event.currentTarget))}
    onFocus={(event) => onSelection(selectedSQL(event.currentTarget))}
    onKeyDown={keyDown}
    onBlur={() => { releaseTab.current = false; }}
    />
    {!readOnly && <p id={help} className="text-xs text-muted-foreground">Tab untuk indentasi. Tekan Escape lalu Tab untuk keluar dari editor.</p>}
  </div>;
}
