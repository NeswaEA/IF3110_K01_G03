import { useEffect, useRef } from "react";
import { Compartment, EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { PostgreSQL, SQLite, sql } from "@codemirror/lang-sql";
import { basicSetup } from "codemirror";
import type { Driver } from "../../api/types";

type Props = {
  value: string;
  driver: Driver;
  readOnly: boolean;
  onChange: (value: string) => void;
  onSelection: (value: string) => void;
};

// TODO: Bikin editor dari scratch tapi contractsnya sama semua (ini so far pake CodeMirror dulu biar yang lain bisa lanjut)
export function SQLEditor(props: Props) {
  const parent = useRef<HTMLDivElement>(null);
  const view = useRef<EditorView | null>(null);
  const readOnly = useRef(new Compartment());
  const callbacks = useRef(props);
  callbacks.current = props;

  useEffect(() => {
    const editor = new EditorView({
      parent: parent.current!,
      state: EditorState.create({
        doc: callbacks.current.value,
        extensions: [
          basicSetup,
          sql({ dialect: props.driver === "postgres" ? PostgreSQL : SQLite }),
          readOnly.current.of([EditorState.readOnly.of(props.readOnly), EditorView.editable.of(!props.readOnly)]),
          EditorView.contentAttributes.of({ "aria-label": "SQL migrasi", role: "textbox", "aria-multiline": "true" }),
          EditorView.lineWrapping,
          EditorView.updateListener.of((update) => {
            if (update.docChanged) callbacks.current.onChange(update.state.doc.toString());
            if (update.docChanged || update.selectionSet) {
              const { from, to, empty } = update.state.selection.main;
              callbacks.current.onSelection(empty ? update.state.doc.toString() : update.state.sliceDoc(from, to));
            }
          }),
        ],
      }),
    });
    view.current = editor;
    callbacks.current.onSelection(editor.state.doc.toString());
    return () => { editor.destroy(); view.current = null; };
  }, [props.driver]);

  useEffect(() => {
    view.current?.dispatch({ effects: readOnly.current.reconfigure([EditorState.readOnly.of(props.readOnly), EditorView.editable.of(!props.readOnly)]) });
  }, [props.readOnly]);

  useEffect(() => {
    const editor = view.current;
    if (editor && editor.state.doc.toString() !== props.value) {
      editor.dispatch({ changes: { from: 0, to: editor.state.doc.length, insert: props.value } });
    }
  }, [props.value]);

  return <div className="migration-sql" ref={parent} />;
}
