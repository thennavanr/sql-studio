import type { Cell, GridColumn } from './grid';

export function h<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  attrs: Record<string, any> = {},
  ...children: (Node | string | null | undefined | false)[]
): HTMLElementTagNameMap[K] {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs)) {
    if (v == null || v === false) continue;
    if (k === 'class') el.className = v;
    else if (k === 'style' && typeof v === 'object') Object.assign(el.style, v);
    else if (k.startsWith('on') && typeof v === 'function') (el as any)[k.toLowerCase()] = v;
    else if (k in el && typeof v !== 'string') (el as any)[k] = v;
    else el.setAttribute(k, v === true ? '' : String(v));
  }
  for (const c of children) {
    if (c == null || c === false) continue;
    el.append(typeof c === 'string' ? document.createTextNode(c) : c);
  }
  return el;
}

export function uid(): string {
  return Math.random().toString(36).slice(2, 10) + Date.now().toString(36).slice(-4);
}

export function errText(e: unknown): string {
  if (e instanceof Error) return e.message;
  return String(e ?? 'Unknown error');
}

export function bracket(name: string): string {
  return '[' + name.replace(/]/g, ']]') + ']';
}

export function formatDuration(ms: number): string {
  if (ms < 1000) return `${ms} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(s < 10 ? 2 : 1)} s`;
  const m = Math.floor(s / 60);
  return `${m}:${String(Math.floor(s % 60)).padStart(2, '0')} min`;
}

// ---- export formats ----

function csvCell(v: Cell): string {
  if (v == null) return '';
  return /[",\r\n]/.test(v) ? '"' + v.replace(/"/g, '""') + '"' : v;
}

export function toCSV(cols: GridColumn[], rows: Cell[][]): string {
  const out = [cols.map((c) => csvCell(c.name)).join(',')];
  for (const r of rows) out.push(r.map(csvCell).join(','));
  return out.join('\r\n') + '\r\n';
}

export function toJSON(cols: GridColumn[], rows: Cell[][]): string {
  const objs = rows.map((r) => {
    const o: Record<string, unknown> = {};
    cols.forEach((c, i) => {
      const v = r[i];
      if (v != null && c.numeric && c.type !== 'bigint' && c.type !== 'decimal' && c.type !== 'numeric') {
        const n = Number(v);
        o[c.name] = Number.isFinite(n) ? n : v;
      } else o[c.name] = v;
    });
    return o;
  });
  return JSON.stringify(objs, null, 2);
}

export function toMarkdown(cols: GridColumn[], rows: Cell[][]): string {
  const esc = (v: Cell) => (v == null ? 'NULL' : v.replace(/\|/g, '\\|').replace(/\r?\n/g, ' '));
  const lines = ['| ' + cols.map((c) => esc(c.name)).join(' | ') + ' |', '| ' + cols.map((c) => (c.numeric ? '---:' : '---')).join(' | ') + ' |'];
  for (const r of rows) lines.push('| ' + r.map(esc).join(' | ') + ' |');
  return lines.join('\n') + '\n';
}

export function toInsert(cols: GridColumn[], rows: Cell[][], table = '[dbo].[TableName]'): string {
  const colList = cols.map((c) => bracket(c.name)).join(', ');
  const lit = (v: Cell, c: GridColumn) => {
    if (v == null) return 'NULL';
    if (c.numeric) return v;
    if (/^(var)?binary|image|timestamp|rowversion/.test(c.type) && v.startsWith('0x')) return v;
    return (/^n/.test(c.type) ? 'N' : '') + "'" + v.replace(/'/g, "''") + "'";
  };
  return rows.map((r) => `INSERT INTO ${table} (${colList}) VALUES (${r.map((v, i) => lit(v, cols[i])).join(', ')});`).join('\n') + '\n';
}

/** Returns the "paragraph" (block separated by blank lines) around pos. */
export function currentStatement(doc: string, pos: number): { from: number; to: number } {
  const lines = doc.split('\n');
  let offset = 0;
  let lineIdx = 0;
  for (; lineIdx < lines.length; lineIdx++) {
    if (offset + lines[lineIdx].length >= pos) break;
    offset += lines[lineIdx].length + 1;
  }
  const blank = (i: number) => i < 0 || i >= lines.length || lines[i].trim() === '' || /^\s*go\s*$/i.test(lines[i]);
  if (blank(lineIdx)) {
    // Cursor on a blank line: use the block just above.
    let i = lineIdx;
    while (i >= 0 && blank(i)) i--;
    if (i < 0) return { from: pos, to: pos };
    lineIdx = i;
  }
  let start = lineIdx;
  while (!blank(start - 1)) start--;
  let end = lineIdx;
  while (!blank(end + 1)) end++;
  let from = 0;
  for (let i = 0; i < start; i++) from += lines[i].length + 1;
  let to = from;
  for (let i = start; i <= end; i++) to += lines[i].length + (i < end ? 1 : 0);
  return { from, to };
}
