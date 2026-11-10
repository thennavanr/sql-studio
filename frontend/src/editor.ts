import { basicSetup } from 'codemirror';
import { EditorView, keymap } from '@codemirror/view';
import { Compartment, EditorState, Prec } from '@codemirror/state';
import { indentWithTab, lineComment, lineUncomment, toggleComment } from '@codemirror/commands';
import { sql, MSSQL } from '@codemirror/lang-sql';
import { javascript, javascriptLanguage } from '@codemirror/lang-javascript';
import type { Completion, CompletionContext, CompletionResult } from '@codemirror/autocomplete';
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

// ---------- MongoDB (mongosh syntax) mode ----------

const COLL_METHODS: [string, string][] = [
  ['find', 'find(filter, projection)'],
  ['findOne', 'findOne(filter)'],
  ['aggregate', 'aggregate([stages])'],
  ['countDocuments', 'countDocuments(filter)'],
  ['estimatedDocumentCount', 'estimatedDocumentCount()'],
  ['distinct', 'distinct(field, filter)'],
  ['insertOne', 'insertOne(doc)'],
  ['insertMany', 'insertMany([docs])'],
  ['updateOne', 'updateOne(filter, update)'],
  ['updateMany', 'updateMany(filter, update)'],
  ['replaceOne', 'replaceOne(filter, doc)'],
  ['deleteOne', 'deleteOne(filter)'],
  ['deleteMany', 'deleteMany(filter)'],
  ['getIndexes', 'getIndexes()'],
  ['createIndex', 'createIndex(keys, options)'],
  ['dropIndex', 'dropIndex(name)'],
  ['stats', 'stats()'],
  ['drop', 'drop()'],
];
const CURSOR_METHODS = ['sort', 'limit', 'skip', 'projection', 'count', 'explain', 'hint', 'collation'];
const DB_METHODS = ['getCollection', 'getCollectionNames', 'getCollectionInfos', 'stats', 'runCommand', 'adminCommand', 'createCollection', 'getName', 'version'];
const OPERATORS = [
  '$eq', '$ne', '$gt', '$gte', '$lt', '$lte', '$in', '$nin', '$and', '$or', '$nor', '$not', '$exists', '$type', '$regex', '$options',
  '$elemMatch', '$size', '$all', '$expr', '$text', '$search', '$set', '$unset', '$inc', '$mul', '$rename', '$min', '$max', '$push',
  '$pull', '$addToSet', '$pop', '$currentDate', '$setOnInsert', '$match', '$group', '$sort', '$project', '$limit', '$skip', '$unwind',
  '$lookup', '$count', '$addFields', '$replaceRoot', '$facet', '$bucket', '$sample', '$out', '$merge', '$sum', '$avg', '$first',
  '$last', '$concat', '$toString', '$dateToString', '$ifNull', '$cond',
];
const HELPERS = ['ObjectId', 'ISODate', 'Long', 'Int32', 'Double', 'Decimal128', 'UUID', 'Timestamp', 'RegExp', 'BinData', 'MinKey', 'MaxKey'];

let mongoSchema: Record<string, string[]> = {};

export function collectionRef(name: string): string {
  return /^[A-Za-z_$][\w$]*(\.[A-Za-z_$][\w$]*)*$/.test(name) ? `db.${name}` : `db.getCollection(${JSON.stringify(name)})`;
}

function mongoComplete(ctx: CompletionContext): CompletionResult | null {
  const line = ctx.state.doc.lineAt(ctx.pos);
  const before = line.text.slice(0, ctx.pos - line.from);
  // db.<collection or db method>
  let m = /\bdb\.([\w$]*)$/.exec(before);
  if (m) {
    const options: Completion[] = [
      ...Object.keys(mongoSchema).map((c) => ({
        label: c,
        type: 'class',
        detail: 'collection',
        apply: /^[A-Za-z_$][\w$.]*$/.test(c) ? c : `getCollection(${JSON.stringify(c)})`,
      })),
      ...DB_METHODS.map((n) => ({ label: n, type: 'method', apply: n + '(' })),
    ];
    return { from: ctx.pos - m[1].length, options, validFor: /^[\w$]*$/ };
  }
  // ...).<cursor modifier>
  m = /\)\s*\.([\w$]*)$/.exec(before);
  if (m) {
    return { from: ctx.pos - m[1].length, options: CURSOR_METHODS.map((n) => ({ label: n, type: 'method', apply: n + '(' })), validFor: /^[\w$]*$/ };
  }
  // db.coll.<method>
  m = /\bdb\.(?:[\w$]+\.)+([\w$]*)$|\bdb\.getCollection\([^)]*\)\.([\w$]*)$/.exec(before);
  if (m) {
    const word = m[1] ?? m[2] ?? '';
    return {
      from: ctx.pos - word.length,
      options: COLL_METHODS.map(([n, d]) => ({ label: n, type: 'method', detail: d, apply: n + '(' })),
      validFor: /^[\w$]*$/,
    };
  }
  // $operators
  m = /\$[\w]*$/.exec(before);
  if (m) {
    return { from: ctx.pos - m[0].length, options: OPERATORS.map((o) => ({ label: o, type: 'keyword' })), validFor: /^\$?\w*$/ };
  }
  const word = ctx.matchBefore(/[\w$.]+/);
  // Field names inside { ... } for the collection referenced in this statement.
  const head = ctx.state.sliceDoc(Math.max(0, ctx.pos - 2000), ctx.pos);
  const ref = [...head.matchAll(/\bdb\.(?:getCollection\((['"])(.+?)\1\)|([\w$]+(?:\.[\w$]+)*?))\.\w+\(/g)].pop();
  const coll = ref ? ref[2] ?? ref[3] : undefined;
  const inKey = /[{,]\s*['"]?[\w$.]*$/.test(before);
  const fields = coll && mongoSchema[coll] ? mongoSchema[coll] : [];
  if (!word && !ctx.explicit && !inKey) return null;
  const options: Completion[] = [
    ...(inKey ? fields.map((f) => ({ label: f, type: 'property', boost: 2 })) : []),
    ...HELPERS.map((n) => ({ label: n, type: 'function', apply: n + '(' })),
    ...['db', 'use', 'show'].map((n) => ({ label: n, type: 'keyword' })),
  ];
  return { from: word ? word.from : ctx.pos, options, validFor: /^[\w$.]*$/ };
}

function mongoLang() {
  return [javascript(), javascriptLanguage.data.of({ autocomplete: mongoComplete })];
}

/** Switch an editor to MongoDB mode; schema maps collection -> field names. */
export function setMongoMode(view: EditorView, schema?: Record<string, string[]>) {
  if (schema) mongoSchema = schema;
  view.dispatch({ effects: langComp.reconfigure(mongoLang()) });
}

/** Switch an editor back to T-SQL mode. */
export function setSQLMode(view: EditorView) {
  view.dispatch({ effects: langComp.reconfigure(sql({ dialect: MSSQL, upperCaseKeywords: true })) });
}

/** A small editor for a single document (edit / insert dialogs). */
export function createDocEditor(parent: HTMLElement, doc: string, fontSize: number): EditorView {
  return new EditorView({
    parent,
    state: EditorState.create({
      doc,
      extensions: [basicSetup, keymap.of([indentWithTab]), mongoLang(), themeComp.of(isDark() ? oneDark : lightTheme), Prec.high(fontTheme(fontSize)), EditorState.tabSize.of(2)],
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

// ---------- comments ----------

/** Comment the selected lines (-- for SQL, // for MongoDB). */
export function commentLines(view: EditorView) {
  lineComment(view);
  view.focus();
}

/** Uncomment the selected lines. */
export function uncommentLines(view: EditorView) {
  lineUncomment(view);
  view.focus();
}

/** Toggle comments on the selected lines (⌘/). */
export function toggleCommentLines(view: EditorView) {
  toggleComment(view);
  view.focus();
}

// ---------- MongoDB formatting ----------

/** Error raised when mongosh text cannot be formatted; line is 1-based in the editor. */
export class FormatError extends Error {
  constructor(
    message: string,
    public line: number,
  ) {
    super(message);
  }
}

const shellCommand = /^\s*(use|show)\s+\S.*$/;

/**
 * Format mongosh statements with Prettier (loaded on first use). Lines such as
 * `use db` and `show collections` are not JavaScript; they are kept as written
 * and the code between them is formatted separately.
 */
export async function formatMongo(view: EditorView) {
  const [prettier, babel, estree] = await Promise.all([
    import('prettier/standalone'),
    import('prettier/plugins/babel'),
    import('prettier/plugins/estree'),
  ]);
  const sel = view.state.selection.main;
  const from = sel.empty ? 0 : view.state.doc.lineAt(sel.from).from;
  const to = sel.empty ? view.state.doc.length : view.state.doc.lineAt(sel.to).to;
  const firstLine = view.state.doc.lineAt(from).number;
  const lines = view.state.sliceDoc(from, to).split('\n');

  const out: string[] = [];
  let chunk: string[] = [];
  let chunkStart = 0;
  const flush = async () => {
    const code = chunk.join('\n');
    chunk = [];
    if (!code.trim()) {
      if (code) out.push(code);
      return;
    }
    try {
      const formatted = await prettier.format(code, {
        parser: 'babel',
        plugins: [babel.default ?? babel, estree.default ?? estree],
        printWidth: 100,
        semi: false,
        quoteProps: 'as-needed',
        trailingComma: 'none',
      });
      out.push(formatted.replace(/\n+$/, ''));
    } catch (e: any) {
      const line = (e?.loc?.start?.line ?? 1) + firstLine - 1 + chunkStart;
      const msg = String(e?.message ?? e).split('\n')[0].replace(/\s*\(\d+:\d+\)$/, '');
      throw new FormatError(`Line ${line}: ${msg}`, line);
    }
  };
  for (let i = 0; i < lines.length; i++) {
    if (shellCommand.test(lines[i])) {
      await flush();
      out.push(lines[i].trim());
      chunkStart = i + 1;
    } else {
      if (!chunk.length) chunkStart = i;
      chunk.push(lines[i]);
    }
  }
  await flush();
  let text = out.join('\n');
  if (sel.empty && !text.endsWith('\n')) text += '\n';
  view.dispatch({ changes: { from, to, insert: text } });
}
