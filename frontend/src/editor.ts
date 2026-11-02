import { basicSetup } from 'codemirror';
import { EditorView, keymap } from '@codemirror/view';
import { Compartment, EditorState, Prec } from '@codemirror/state';
import { indentWithTab } from '@codemirror/commands';
import { sql, MSSQL } from '@codemirror/lang-sql';
import { oneDark } from '@codemirror/theme-one-dark';

export interface EditorHandlers {
  onExecute: () => void;
  onExecuteCurrent: () => void;
  onChange: () => void;
}

const themeComp = new Compartment();
const langComp = new Compartment();
const fontComp = new Compartment();

const lightTheme = EditorView.theme({}, { dark: false });

function fontTheme(size: number) {
  return EditorView.theme({
    '&': { fontSize: size + 'px', backgroundColor: 'var(--bg)' },
    '.cm-gutters': { backgroundColor: 'var(--bg)', borderRight: '1px solid var(--border-soft)' },
    '.cm-content, .cm-gutters': { fontFamily: 'var(--mono)' },
  });
}

export function isDark(): boolean {
  return document.documentElement.dataset.resolvedTheme === 'dark';
}

export function createEditor(parent: HTMLElement, doc: string, fontSize: number, h: EditorHandlers): EditorView {
  const run = () => {
    h.onExecute();
    return true;
  };
  const runCurrent = () => {
    h.onExecuteCurrent();
    return true;
  };
  return new EditorView({
    parent,
    state: EditorState.create({
      doc,
      extensions: [
        basicSetup,
        Prec.highest(
          keymap.of([
            { key: 'Mod-e', run },
            { key: 'Mod-Enter', run },
            { key: 'F5', run },
            { key: 'Shift-Mod-e', run: runCurrent },
            { key: 'Shift-Mod-Enter', run: runCurrent },
          ]),
        ),
        keymap.of([indentWithTab]),
        langComp.of(sql({ dialect: MSSQL, upperCaseKeywords: true })),
        themeComp.of(isDark() ? oneDark : lightTheme),
        Prec.high(fontComp.of(fontTheme(fontSize))),
        EditorView.updateListener.of((u) => {
          if (u.docChanged) h.onChange();
        }),
        EditorState.tabSize.of(4),
      ],
    }),
  });
}

/** Feed table/column names to autocompletion. Keys are "schema.table". */
export function setCompletionSchema(view: EditorView, schema: Record<string, string[]>, defaultSchema = 'dbo') {
  view.dispatch({
    effects: langComp.reconfigure(sql({ dialect: MSSQL, upperCaseKeywords: true, schema, defaultSchema })),
  });
}

export function setEditorTheme(view: EditorView) {
  view.dispatch({ effects: themeComp.reconfigure(isDark() ? oneDark : lightTheme) });
}

export function setEditorFont(view: EditorView, size: number) {
  view.dispatch({ effects: fontComp.reconfigure(fontTheme(size)) });
}

/** Format the selection, or the whole document, as T-SQL. */
export async function formatSQL(view: EditorView) {
  const { format } = await import('sql-formatter');
  const sel = view.state.selection.main;
  const from = sel.empty ? 0 : sel.from;
  const to = sel.empty ? view.state.doc.length : sel.to;
  const text = view.state.sliceDoc(from, to);
  const formatted = format(text, { language: 'transactsql', keywordCase: 'upper', tabWidth: 4, linesBetweenQueries: 2 });
  view.dispatch({ changes: { from, to, insert: formatted } });
}

export function gotoLine(view: EditorView, line: number) {
  const n = Math.min(Math.max(1, line), view.state.doc.lines);
  const l = view.state.doc.line(n);
  view.dispatch({ selection: { anchor: l.from, head: l.to }, scrollIntoView: true });
  view.focus();
}
