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
    pre.innerHTML = highlight(this.info.docs[i]);
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
    card.addEventListener('dblclick', () => {
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
        pre.innerHTML = highlight(stored);
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
