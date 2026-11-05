// MongoDB result rendering: a Compass-style list of documents with in-place
// Edit / Copy / Delete, plus the document editor used for editing and inserts.
import type { EditorView } from '@codemirror/view';
import * as App from './wailsjs/go/main/App';
import { confirmDialog, modal, showErrorDialog } from './dialogs';
import { createDocEditor } from './editor';
import { state } from './state';
import { errText, h } from './util';
import { icon } from './icons';

export interface DocsInfo {
  docs: string[];
  json: string[];
  ids: string[];
  collection: string;
  database: string;
  editable: boolean;
  truncated: boolean;
  limit: number;
}

const PAGE = 200;

function esc(s: string): string {
  return s.replace(/[&<>]/g, (c) => (c === '&' ? '&amp;' : c === '<' ? '&lt;' : '&gt;'));
}

/** Lightweight syntax highlighting for mongosh-style documents. */
export function highlight(text: string): string {
  const re =
    /('(?:[^'\\\n]|\\.)*')(\s*:)?|\b(ObjectId|ISODate|Long|Int32|Double|Decimal128|UUID|BinData|Timestamp|MinKey|MaxKey|Code)\b|(-?\b\d+(?:\.\d+)?(?:e[+-]?\d+)?\b)|\b(true|false|null|undefined|NaN|Infinity)\b|^(\s*)([A-Za-z_$][\w$]*)(?=: )|(\/(?:[^/\\\n]|\\.)+\/[a-z]*)/gm;
  let out = '';
  let last = 0;
  for (const m of text.matchAll(re)) {
    out += esc(text.slice(last, m.index));
    if (m[1] !== undefined) {
      out += m[2] !== undefined ? `<span class="k">${esc(m[1])}</span>${esc(m[2])}` : `<span class="s">${esc(m[1])}</span>`;
    } else if (m[3]) out += `<span class="f">${m[3]}</span>`;
    else if (m[4]) out += `<span class="n">${m[4]}</span>`;
    else if (m[5]) out += `<span class="b">${m[5]}</span>`;
    else if (m[7]) out += `${m[6]}<span class="k">${esc(m[7])}</span>`;
    else if (m[8]) out += `<span class="r">${esc(m[8])}</span>`;
    last = (m.index ?? 0) + m[0].length;
  }
  return out + esc(text.slice(last));
}

// ---------- collapsible tree rendering ----------

/** Nested blocks longer than this many lines start collapsed. */
const AUTO_COLLAPSE_LINES = 20;

function indentOf(line: string): number {
  return line.length - line.trimStart().length;
}

/** True when a printed line opens a multi-line object or array. */
function opensBlock(rest: string): boolean {
  return rest.endsWith('{') || rest.endsWith('[');
}

function lineEl(indent: string, toggle: HTMLElement | null, rest: string, cls = 'dl'): HTMLElement {
  const el = h('div', { class: cls });
  el.append(indent, toggle ?? h('span', { class: 'tw-gap' }));
  const body = h('span', {});
  body.innerHTML = highlight(rest);
  el.append(body);
  return el;
}

/**
 * Renders mongosh-style pretty text as a collapsible tree. The printer puts
 * every multi-line object/array opener at a line end and its closer on its own
 * line at the same indentation, and never emits raw newlines inside strings,
 * so blocks can be recovered from the lines alone.
 */
export function renderTree(target: HTMLElement, text: string, collapseAll?: boolean) {
  const lines = text.split('\n');
  const build = (from: number, to: number, depth: number): DocumentFragment => {
    const frag = document.createDocumentFragment();
    for (let i = from; i < to; i++) {
      const line = lines[i];
      const ind = indentOf(line);
      const rest = line.slice(ind);
      if (!opensBlock(rest)) {
        frag.append(lineEl(line.slice(0, ind), null, rest));
        continue;
      }
      // find the matching closer at the same indentation
      let j = i + 1;
      while (j < to && !(indentOf(lines[j]) === ind && /^[}\]]/.test(lines[j].slice(ind)))) j++;
      if (j >= to) {
        frag.append(lineEl(line.slice(0, ind), null, rest));
        continue;
      }
      const isArr = rest.endsWith('[');
      const closeRest = lines[j].slice(ind);
      // count direct children (lines one level deeper that start an entry)
      let n = 0;
      for (let k = i + 1; k < j; k++) {
        if (indentOf(lines[k]) === ind + 2 && !/^[}\]]/.test(lines[k].slice(ind + 2))) n++;
      }
      const label = isArr ? `${n} item${n === 1 ? '' : 's'}` : `${n} field${n === 1 ? '' : 's'}`;
      const tw = h('span', { class: 'tw', title: 'Collapse / expand (⌥-click: all inside)' });
      const head = lineEl(line.slice(0, ind), tw, rest, 'dl head');
      const sum = h('span', { class: 'sum' });
      const first = i + 1 < j ? lines[i + 1].slice(ind + 2) : '';
      if (depth === 0 && first && !opensBlock(first) && n > 0) {
        // top-level document: preview its first field (usually _id)
        const preview = h('span', {});
        preview.innerHTML = ' ' + highlight(first.replace(/,$/, ''));
        sum.append(preview, n > 1 ? ` … ${n - 1} more ` : ' ', h('span', { class: 'p' }, closeRest));
      } else {
        sum.append(` … ${label} `, h('span', { class: 'p' }, closeRest));
      }
      head.append(sum);
      const blk = h('div', { class: 'blk' }, head, h('div', { class: 'kids' }, build(i + 1, j, depth + 1)), lineEl(line.slice(0, ind), null, closeRest, 'dl close'));
      const span = j - i;
      if (collapseAll === true || (collapseAll === undefined && depth > 0 && span > AUTO_COLLAPSE_LINES)) blk.classList.add('collapsed');
      tw.addEventListener('click', (e) => {
        e.stopPropagation();
        const collapse = !blk.classList.contains('collapsed');
        blk.classList.toggle('collapsed', collapse);
        if ((e as MouseEvent).altKey) blk.querySelectorAll('.blk').forEach((b) => b.classList.toggle('collapsed', collapse));
      });
      // clicking the summary of a collapsed block expands it
      sum.addEventListener('click', (e) => {
        e.stopPropagation();
        blk.classList.remove('collapsed');
      });
      frag.append(blk);
      i = j;
    }
    return frag;
  };
  target.replaceChildren(build(0, lines.length, 0));
}

/**
 * Opens a document editor. onSave receives the text and returns an error
 * message to keep the dialog open, or null when saved.
 */
export async function documentEditor(title: string, text: string, saveLabel: string, onSave: (text: string) => Promise<string | null>): Promise<boolean> {
  const host = h('div', { class: 'doc-editor' });
  const status = h('div', { class: 'test-status' });
  const ed: { view: EditorView | null } = { view: null };
  const body = h(
    'div',
    { class: 'form' },
    host,
    h('p', { class: 'hint' }, "mongosh syntax: ObjectId('…'), ISODate('…'), Long('…'), Decimal128('…'), NumberInt(…), /regex/i. Comments are allowed."),
    status,
  );
  queueMicrotask(() => {
    ed.view = createDocEditor(host, text, state.settings.fontSize);
    ed.view.focus();
  });
  let saved = false;
  await modal(
    title,
    body,
    [
      { label: 'Cancel', value: 'cancel' },
      { label: saveLabel, value: 'save', primary: true },
    ],
    {
      wide: true,
      onButton: async (v) => {
        if (v !== 'save') return true;
        status.className = 'test-status';
        status.textContent = 'Saving…';
        const err = await onSave(ed.view?.state.doc.toString() ?? text);
        if (err) {
          status.className = 'test-status err';
          status.textContent = err;
          return false;
        }
        saved = true;
        return true;
      },
    },
  );
  ed.view?.destroy();
  return saved;
}

/** Insert dialog; resolves with the new _id (shell syntax) or null. */
export async function insertDocumentDialog(connId: string, db: string, coll: string): Promise<string | null> {
  let id: string | null = null;
  await documentEditor(`Insert into ${db}.${coll}`, '{\n  \n}', 'Insert', async (text) => {
    try {
      id = await App.MongoInsertDocument(connId, db, coll, text);
      return null;
    } catch (e) {
      return errText(e);
    }
  });
  return id;
}

export class DocsView {
  readonly el: HTMLElement;
  private shown = 0;
  private collapseMode: boolean | undefined; // undefined = automatic
  private list: HTMLElement;
  private more: HTMLButtonElement;

  constructor(
    private connId: string,
    private info: DocsInfo,
    private onChange: (msg: string) => void,
  ) {
    this.list = h('div', { class: 'doc-list' });
    this.more = h('button', { class: 'btn small', onclick: () => this.renderMore() }, 'Show more');
    this.el = h('div', { class: 'docs-view' }, this.list, h('div', { class: 'doc-more' }, this.more));
    this.renderMore();
  }

  /** Expands or collapses every node in every document. */
  setAll(expanded: boolean) {
    this.collapseMode = !expanded;
    this.list.querySelectorAll('.blk').forEach((b) => b.classList.toggle('collapsed', !expanded));
  }

  private renderMore() {
    const end = Math.min(this.info.docs.length, this.shown + PAGE);
    const frag = document.createDocumentFragment();
    for (let i = this.shown; i < end; i++) frag.append(this.card(i));
    this.list.append(frag);
    this.shown = end;
    const left = this.info.docs.length - this.shown;
    this.more.parentElement!.hidden = left <= 0;
    this.more.textContent = `Show ${Math.min(PAGE, left)} more (${left.toLocaleString()} remaining)`;
    if (!this.info.docs.length) this.list.replaceChildren(h('div', { class: 'results-empty' }, 'No documents.'));
  }

  private card(i: number): HTMLElement {
    const pre = h('pre', { class: 'doc-pre' });
    renderTree(pre, this.info.docs[i], this.collapseMode);
    const copy = () => void App.CopyText(this.info.docs[i]);
    const actions = h(
      'div',
      { class: 'doc-actions' },
      h('button', { class: 'icon-btn', title: 'Copy document', onclick: copy }, icon('copy', 13)),
    );
    const card = h('div', { class: 'doc-card' }, h('div', { class: 'doc-num' }, String(i + 1)), pre, actions);
    if (this.info.editable && this.info.collection && this.info.ids[i]) {
      actions.append(
        h('button', { class: 'icon-btn', title: 'Edit document', onclick: () => void this.edit(i, card, pre) }, icon('edit', 13)),
        h('button', { class: 'icon-btn danger', title: 'Delete document', onclick: () => void this.remove(i, card) }, icon('trash', 13)),
      );
    }
    card.addEventListener('dblclick', (e) => {
      if ((e.target as HTMLElement).closest('.tw, .sum')) return;
      if (this.info.editable && !card.classList.contains('deleted')) void this.edit(i, card, pre);
    });
    return card;
  }

  private async edit(i: number, card: HTMLElement, pre: HTMLElement) {
    const { database, collection } = this.info;
    const id = this.info.ids[i];
    await documentEditor(`Edit document in ${database}.${collection}`, this.info.docs[i], 'Save', async (text) => {
      try {
        const stored = await App.MongoReplaceDocument(this.connId, database, collection, id, text);
        this.info.docs[i] = stored;
        renderTree(pre, stored, this.collapseMode);
        card.classList.add('changed');
        this.onChange(`Updated document ${id} in ${collection}. Re-run the query to refresh the Table view.`);
        return null;
      } catch (e) {
        return errText(e);
      }
    });
  }

  private async remove(i: number, card: HTMLElement) {
    const { database, collection } = this.info;
    const id = this.info.ids[i];
    if (!(await confirmDialog('Delete document', `Delete the document with _id ${id} from ${database}.${collection}? This cannot be undone.`, 'Delete', true))) return;
    try {
      await App.MongoDeleteDocument(this.connId, database, collection, id);
      card.classList.add('deleted');
      card.querySelectorAll<HTMLButtonElement>('.doc-actions button').forEach((b, n) => (b.disabled = n > 0));
      this.onChange(`Deleted document ${id} from ${collection}.`);
    } catch (e) {
      showErrorDialog('Delete failed', errText(e));
    }
  }
}
