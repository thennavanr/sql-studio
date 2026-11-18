// Context-aware T-SQL completion: suggests the columns of the tables a
// statement references (FROM / JOIN / UPDATE / INTO / MERGE), resolves
// aliases for `alias.` completion, and qualifies column names that exist in
// more than one referenced table.
import type { Completion, CompletionContext, CompletionResult } from '@codemirror/autocomplete';

/** "schema.table" -> column names, as returned by the backend. */
export type SqlSchema = Record<string, string[]>;

export interface TableRef {
  key: string; // schema key, e.g. "dbo.orders"
  alias: string; // alias as written, or the table's own name
}

interface Token {
  text: string;
  upper: string;
  kind: 'ident' | 'punct' | 'other';
  start: number;
  end: number;
}

const CLAUSE_END = new Set([
  'WHERE', 'GROUP', 'ORDER', 'HAVING', 'UNION', 'EXCEPT', 'INTERSECT', 'OPTION', 'FOR', 'SET', 'VALUES',
  'OUTPUT', 'SELECT', 'INSERT', 'UPDATE', 'DELETE', 'MERGE', 'WITH', 'ON', 'WHEN', 'USING', 'RETURN',
]);
const NOT_ALIAS = new Set([
  ...CLAUSE_END, 'JOIN', 'INNER', 'LEFT', 'RIGHT', 'FULL', 'CROSS', 'OUTER', 'APPLY', 'AS', 'PIVOT',
  'UNPIVOT', 'TABLESAMPLE', 'WITH', 'NOLOCK', 'GO', 'BEGIN', 'END', 'IF', 'ELSE', 'AND', 'OR', 'INTO',
  'FROM', 'TOP', 'DISTINCT', 'BY', 'TABLE', 'EXEC', 'EXECUTE', 'DECLARE', 'CREATE', 'ALTER', 'DROP',
]);
/** After these keywords a table name (not a column) is expected. */
const TABLE_CONTEXT = new Set(['FROM', 'JOIN', 'INTO', 'UPDATE', 'TABLE', 'MERGE', 'APPLY', 'EXEC', 'EXECUTE', 'USE', 'TRUNCATE']);

/** Tokenizes T-SQL, skipping comments and string literals. */
export function tokenize(text: string): Token[] {
  const out: Token[] = [];
  let i = 0;
  const n = text.length;
  while (i < n) {
    const c = text[i];
    if (/\s/.test(c)) {
      i++;
    } else if (c === '-' && text[i + 1] === '-') {
      while (i < n && text[i] !== '\n') i++;
    } else if (c === '/' && text[i + 1] === '*') {
      const end = text.indexOf('*/', i + 2);
      i = end < 0 ? n : end + 2;
    } else if (c === "'" || ((c === 'N' || c === 'n') && text[i + 1] === "'")) {
      const start = i;
      i += c === "'" ? 1 : 2;
      while (i < n) {
        if (text[i] === "'" && text[i + 1] === "'") i += 2;
        else if (text[i] === "'") {
          i++;
          break;
        } else i++;
      }
      out.push({ text: text.slice(start, i), upper: '', kind: 'other', start, end: i });
    } else if (c === '[' || c === '"') {
      const close = c === '[' ? ']' : '"';
      const start = i;
      i++;
      while (i < n) {
        if (text[i] === close && text[i + 1] === close) i += 2;
        else if (text[i] === close) {
          i++;
          break;
        } else i++;
      }
      const raw = text.slice(start, i);
      const name = raw.slice(1, raw.endsWith(close) ? -1 : undefined);
      out.push({ text: name, upper: '\u0000' + name.toUpperCase(), kind: 'ident', start, end: i }); // quoted: never a keyword
    } else if (/[\w@#$]/.test(c)) {
      const start = i;
      while (i < n && /[\w@#$]/.test(text[i])) i++;
      const t = text.slice(start, i);
      out.push({ text: t, upper: t.toUpperCase(), kind: 'ident', start, end: i });
    } else {
      out.push({ text: c, upper: c, kind: 'punct', start: i, end: i + 1 });
      i++;
    }
  }
  return out;
}

/** Bounds of the statement around pos: between `;` / GO lines / blank lines. */
export function statementBounds(doc: string, pos: number): { from: number; to: number } {
  const sep = /;|^\s*GO\s*$|\n[ \t]*\n/gim;
  let from = 0;
  let to = doc.length;
  for (const m of doc.matchAll(sep)) {
    const s = m.index ?? 0;
    const e = s + m[0].length;
    if (e <= pos) from = e;
    else if (s >= pos) {
      to = s;
      break;
    } else if (s < pos && e > pos) {
      // cursor inside a separator (e.g. on a blank line)
      from = e;
    }
  }
  return { from, to };
}

/** Builds a lookup from lower-cased names to schema keys. */
function indexSchema(schema: SqlSchema, defaultSchema: string) {
  const byFull = new Map<string, string>();
  const byName = new Map<string, string[]>();
  for (const key of Object.keys(schema)) {
    const lower = key.toLowerCase();
    byFull.set(lower, key);
    const name = lower.slice(lower.indexOf('.') + 1);
    byName.set(name, [...(byName.get(name) ?? []), key]);
  }
  return (parts: string[]): string | undefined => {
    const p = parts.map((x) => x.toLowerCase());
    if (p.length >= 2) return byFull.get(`${p[p.length - 2]}.${p[p.length - 1]}`);
    const keys = byName.get(p[0]);
    if (!keys) return undefined;
    return keys.find((k) => k.toLowerCase() === `${defaultSchema.toLowerCase()}.${p[0]}`) ?? (keys.length === 1 ? keys[0] : undefined);
  };
}

/** Finds the tables referenced by a statement, with their aliases. */
export function tableRefs(tokens: Token[], schema: SqlSchema, defaultSchema = 'dbo'): TableRef[] {
  const resolve = indexSchema(schema, defaultSchema);
  const refs: TableRef[] = [];
  let inFrom = false;
  let depth = 0;
  let fromDepth = 0;
  for (let i = 0; i < tokens.length; i++) {
    const t = tokens[i];
    if (t.text === '(') depth++;
    else if (t.text === ')') depth--;
    let expectTable = false;
    if (t.kind === 'ident' && (t.upper === 'FROM' || t.upper === 'JOIN' || t.upper === 'UPDATE' || t.upper === 'INTO' || t.upper === 'APPLY')) {
      expectTable = true;
      inFrom = t.upper === 'FROM' || t.upper === 'JOIN';
      fromDepth = depth;
    } else if (t.upper === 'MERGE') {
      expectTable = true;
      if (tokens[i + 1]?.upper === 'INTO') i++;
    } else if (t.text === ',' && inFrom && depth === fromDepth) {
      expectTable = true;
    } else if (t.kind === 'ident' && CLAUSE_END.has(t.upper) && depth <= fromDepth) {
      inFrom = false;
    }
    if (!expectTable) continue;
    // [schema.]table, possibly db.schema.table
    const next = tokens[i + 1];
    if (!next || next.kind !== 'ident' || NOT_ALIAS.has(next.upper)) continue;
    const parts = [next.text];
    let j = i + 2;
    while (tokens[j]?.text === '.' && tokens[j + 1]?.kind === 'ident') {
      parts.push(tokens[j + 1].text);
      j += 2;
    }
    const key = resolve(parts);
    // optional alias: [AS] name
    let alias = parts[parts.length - 1];
    let k = j;
    if (tokens[k]?.upper === 'AS') k++;
    const a = tokens[k];
    if (a && a.kind === 'ident' && !NOT_ALIAS.has(a.upper)) {
      alias = a.text;
      k++;
    }
    if (key) refs.push({ key, alias });
    i = k - 1;
  }
  return refs;
}

/** True when the cursor is where a table name (not a column) belongs. */
function expectsTable(before: Token[], rel: number): boolean {
  for (let i = before.length - 1; i >= 0; i--) {
    const t = before[i];
    if (t.kind !== 'ident' && t.text !== '.') return t.text === ',' ? commaInFrom(before, i) : false;
    if (t.kind === 'ident') {
      const beingTyped = i === before.length - 1 && t.end === rel;
      if (!beingTyped && TABLE_CONTEXT.has(t.upper)) return true;
      // the word being typed or a qualifier: keep looking one step back
      if (beingTyped || before[i + 1]?.text === '.') continue;
      return false;
    }
  }
  return false;
}

function commaInFrom(before: Token[], commaIndex: number): boolean {
  let depth = 0;
  for (let i = commaIndex - 1; i >= 0; i--) {
    const t = before[i];
    if (t.text === ')') depth++;
    else if (t.text === '(') {
      if (depth === 0) return false;
      depth--;
    } else if (depth === 0 && t.kind === 'ident') {
      if (t.upper === 'FROM') return true;
      if (CLAUSE_END.has(t.upper)) return false;
    }
  }
  return false;
}

const columnBoost = 10;

/**
 * Completion source for columns. getSchema returns the schema for the
 * editor (per tab), so tabs on different databases never mix suggestions.
 */
export function sqlColumnCompletion(getSchema: (ctx: CompletionContext) => SqlSchema, defaultSchema = 'dbo') {
  return (ctx: CompletionContext): CompletionResult | null => {
    const schema = getSchema(ctx);
    if (!schema || !Object.keys(schema).length) return null;
    const doc = ctx.state.doc.toString();
    const { from, to } = statementBounds(doc, ctx.pos);
    const stmt = doc.slice(from, to);
    const rel = ctx.pos - from;
    const tokens = tokenize(stmt);
    // Not inside a string or comment.
    const inside = tokens.find((t) => t.start < rel && t.end > rel && t.kind === 'other');
    if (inside) return null;
    const lineBefore = stmt.slice(0, rel);
    if (/--[^\n]*$/.test(lineBefore) || lineBefore.lastIndexOf('/*') > lineBefore.lastIndexOf('*/')) return null;

    const refs = tableRefs(tokens, schema, defaultSchema);
    if (!refs.length) return null;

    // `alias.` / `table.` qualifiers are completed by the SQL extension
    // itself (it resolves aliases); avoid offering the same columns twice.
    if (/\.[\w@#$]*$/.test(lineBefore)) return null;

    const before = tokens.filter((t) => t.end <= rel);
    if (expectsTable(before, rel)) return null;
    const word = ctx.matchBefore(/[\w@#$]+/);
    if (!word && !ctx.explicit) return null;

    // Column name -> tables that have it.
    const owners = new Map<string, TableRef[]>();
    for (const r of refs) {
      for (const c of schema[r.key] ?? []) {
        const k = c.toLowerCase();
        const list = owners.get(k) ?? [];
        if (!list.some((x) => x.alias === r.alias)) list.push(r);
        owners.set(k, list);
      }
    }
    const options: Completion[] = [];
    const multiTable = refs.length > 1;
    for (const r of refs) {
      for (const c of schema[r.key] ?? []) {
        const shared = (owners.get(c.toLowerCase())?.length ?? 0) > 1;
        if (shared) {
          // Same name in several tables: match on the column name, insert it qualified.
          options.push({ label: c, apply: `${r.alias}.${c}`, type: 'property', detail: `${r.alias}.${c} · ${r.key}`, boost: columnBoost });
        } else {
          options.push({ label: c, type: 'property', detail: multiTable ? `${r.alias} · ${r.key}` : r.key, boost: columnBoost });
        }
      }
    }
    return { from: word ? word.from : ctx.pos, options, validFor: /^[\w@#$]*$/ };
  };
}
