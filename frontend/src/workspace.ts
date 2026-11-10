import type { EditorView } from '@codemirror/view';
import * as App from './wailsjs/go/main/App';
import { EventsOn } from './wailsjs/runtime/runtime';
import { DataGrid, type Cell, type GridColumn } from './grid';
import { commentLines, createEditor, FormatError, formatMongo, formatSQL, toggleCommentLines, uncommentLines, gotoLine, setCompletionSchema, setEditorFont, setEditorTheme, setMongoMode, setSQLMode } from './editor';
import { DocsView, insertDocumentDialog, type DocsInfo } from './docs';
import { cellViewer, confirmDialog, connectionDialog, contextMenu, infoDialog, promptText, showErrorDialog } from './dialogs';
import { emit, ensureConnected, on, state } from './state';
import { currentStatement, errText, formatDuration, h, toCSV, toInsert, toJSON, toMarkdown, uid } from './util';
import { icon } from './icons';

interface ResultSet {
  grid: DataGrid;
  rowCount: number;
  truncated: boolean;
  done: boolean;
  docs?: DocsInfo; // MongoDB results
  docsView?: DocsView;
  view: 'docs' | 'table';
}

interface Summary {
  elapsedMs: number;
  batches: number;
  errors: number;
  resultSets: number;
  cancelled: boolean;
  database: string;
}

interface Msg {
  kind: string;
  text: string;
  line?: number;
  time: number;
}

class QueryTab {
  id = uid();
  title: string;
  path = '';
  connId = '';
  database = '';
  dirty = false;
  running = false;
  runToken = '';
  mode: 'sql' | 'mongo' = 'sql';
  nextSeq = 0;
  pending = new Map<number, { name: string; d: any }>();
  startedAt = 0;
  lineOffset = 0;
  results: ResultSet[] = [];
  messages: Msg[] = [];
  summary?: Summary;
  pane: 'results' | 'messages' = 'results';
  activeResult = 0;
  resultsVisible = false;
  view!: EditorView;
  el: HTMLElement;
  editorHost: HTMLElement;
  resultsEl: HTMLElement;
  resultsHead: HTMLElement;
  resultsBody: HTMLElement;

  constructor(title: string) {
    this.title = title;
    this.editorHost = h('div', { class: 'editor-host' });
    this.resultsHead = h('div', { class: 'results-head' });
    this.resultsBody = h('div', { class: 'results-body' });
    this.resultsEl = h('section', { class: 'results' }, this.resultsHead, this.resultsBody);
    this.el = h('div', { class: 'tab-pane' }, this.editorHost, h('div', { class: 'hsplit' }), this.resultsEl);
  }
}

const SESSION_KEY = 'sqlstudio.tabs.v1';

export class Workspace {
  readonly el: HTMLElement;
  private tabs: QueryTab[] = [];
  private active: QueryTab | null = null;
  private tabBar: HTMLElement;
  private panes: HTMLElement;
  private welcome: HTMLElement;
  private connSelect: HTMLSelectElement;
  private dbSelect: HTMLSelectElement;
  private runBtn: HTMLButtonElement;
  private cancelBtn: HTMLButtonElement;
  private statusLeft: HTMLElement;
  private statusRight: HTMLElement;
  private selInfo = '';
  private counter = 0;
  private schemaCache = new Map<string, Promise<Record<string, string[]>>>();
  private resultsFrac = 0.45;
  private mongoView: 'docs' | 'table' = 'docs';
  private ticker = 0;
  private saveTimer = 0;
  readonly toolbar: HTMLElement;
  readonly statusbar: HTMLElement;

  constructor(private onNewConnection: () => void) {
    this.connSelect = h('select', { class: 'input tb-select', title: 'Connection' });
    this.dbSelect = h('select', { class: 'input tb-select db', title: 'Database' });
    this.runBtn = h('button', { class: 'btn run', title: 'Execute (⌘E / ⌘↵ / F5)', onclick: () => this.execute('auto') }, icon('play'), 'Run');
    this.cancelBtn = h('button', { class: 'btn', title: 'Cancel (⌘.)', disabled: true, onclick: () => this.cancel() }, icon('stop'), 'Cancel');
    const tool = (name: string, label: string, title: string, fn: () => void) =>
      h('button', { class: 'btn tool', title, onclick: fn }, icon(name, 13), h('span', { class: 'tool-label' }, label));
    this.toolbar = h(
      'div',
      { class: 'toolbar querybar' },
      this.runBtn,
      this.cancelBtn,
      h('span', { class: 'tb-sep' }),
      h('span', { class: 'node-icon' }, icon('server')),
      this.connSelect,
      h('span', { class: 'node-icon' }, icon('database')),
      this.dbSelect,
      h('span', { class: 'tb-sep' }),
      tool('check', 'Validate', 'Check the query without running it (⇧⌘P)', () => void this.validate()),
      tool('format', 'Format', 'Format the selection or the whole query (⌥⇧F)', () => void this.format()),
      tool('comment', 'Comment', 'Comment the selected lines', () => this.comment('add')),
      tool('uncomment', 'Uncomment', 'Uncomment the selected lines', () => this.comment('remove')),
    );
    this.connSelect.addEventListener('change', () => this.onConnChange());
    this.dbSelect.addEventListener('change', () => {
      if (!this.active) return;
      this.active.database = this.dbSelect.value;
      this.loadSchema(this.active);
      this.persist();
      this.updateStatus();
    });

    this.tabBar = h('div', { class: 'tabbar' });
    this.tabBar.addEventListener('dblclick', (e) => {
      if (e.target === this.tabBar) this.newTab();
    });
    this.panes = h('div', { class: 'panes' });
    this.welcome = this.buildWelcome();
    this.panes.appendChild(this.welcome);
    this.el = h('div', { class: 'workspace' }, this.tabBar, this.toolbar, this.panes);

    this.statusLeft = h('div', { class: 'status-left' });
    this.statusRight = h('div', { class: 'status-right' });
    this.statusbar = h('footer', { class: 'statusbar' }, this.statusLeft, this.statusRight);

    on('connections', () => {
      for (const x of this.tabs) this.applyMode(x);
      // A tab without a connection adopts the first one that gets connected.
      const t = this.active;
      if (t && (!t.connId || !state.conns.has(t.connId))) {
        const c = this.firstConnected();
        if (c) {
          this.setConnection(t, c);
          return;
        }
      }
      this.renderToolbar();
      this.updateStatus();
    });
    on('settings', () => {
      for (const t of this.tabs) {
        setEditorTheme(t.view);
        setEditorFont(t.view, state.settings.fontSize);
      }
    });
    this.listen();
    try {
      const f = Number(localStorage.getItem('sqlstudio.resultsFrac'));
      if (f > 0.1 && f < 0.9) this.resultsFrac = f;
    } catch {
      /* storage unavailable */
    }
  }

  private buildWelcome() {
    const kb = (k: string, d: string) => h('div', { class: 'kb' }, h('kbd', {}, k), h('span', {}, d));
    return h(
      'div',
      { class: 'welcome' },
      h('h1', {}, 'SQL Studio'),
      h('p', { class: 'muted' }, 'A lightweight client for SQL Server, Azure SQL and MongoDB.'),
      h(
        'div',
        { class: 'welcome-actions' },
        h('button', { class: 'btn primary', onclick: () => this.onNewConnection() }, 'New Connection'),
        h('button', { class: 'btn', onclick: () => this.newTab() }, 'New Query'),
        h('button', { class: 'btn', onclick: () => void this.openFile() }, 'Open File…'),
      ),
      h(
        'div',
        { class: 'shortcuts' },
        kb('⌘N', 'New query'),
        kb('⌘E  ⌘↵  F5', 'Run (selection or all)'),
        kb('⇧⌘E', 'Run current statement'),
        kb('⌘.', 'Cancel'),
        kb('⌥⇧F', 'Format SQL'),
        kb('⌘R', 'Toggle results'),
        kb('⌘B', 'Toggle sidebar'),
        kb('⌘Y', 'History'),
      ),
    );
  }

  // ---------- tabs ----------

  newTab(opts: { connId?: string; database?: string; text?: string; title?: string; path?: string; lazy?: boolean } = {}): QueryTab {
    const t = new QueryTab(opts.title ?? `Query ${++this.counter}`);
    t.path = opts.path ?? '';
    const fallbackConn = opts.connId ?? this.active?.connId ?? this.firstConnected();
    t.connId = fallbackConn && state.conns.has(fallbackConn) ? fallbackConn : '';
    t.database = opts.database ?? (t.connId === this.active?.connId ? this.active?.database : '') ?? '';
    if (t.connId && !t.database) {
      const cs = state.conns.get(t.connId);
      t.database = cs?.profile.database || cs?.info?.database || '';
    }
    this.tabs.push(t);
    this.panes.appendChild(t.el);
    t.view = createEditor(t.editorHost, opts.text ?? '', state.settings.fontSize, {
      onExecute: () => this.execute('auto'),
      onExecuteCurrent: () => this.execute('current'),
      onChange: () => {
        if (!t.dirty) {
          t.dirty = true;
          this.renderTabs();
        }
        this.persist();
      },
    });
    this.setupSplitter(t);
    this.applyMode(t);
    this.activate(t);
    if (t.connId && !opts.lazy) void ensureConnected(t.connId).then((ok) => ok && this.loadSchema(t));
    this.persist();
    return t;
  }

  private firstConnected(): string {
    for (const [id, cs] of state.conns) if (cs.status === 'connected') return id;
    return '';
  }

  activate(t: QueryTab) {
    this.active = t;
    for (const x of this.tabs) x.el.classList.toggle('active', x === t);
    this.welcome.hidden = true;
    this.renderTabs();
    this.renderToolbar();
    this.renderResults(t);
    this.layout(t);
    this.updateStatus();
    requestAnimationFrame(() => t.view.focus());
    emit('activeTab');
  }

  async closeTab(t: QueryTab | null = this.active) {
    if (!t) return;
    if (t.dirty && t.view.state.doc.length > 0) {
      const ok = await confirmDialog('Close tab', `“${t.title}” has unsaved changes. Close anyway?`, 'Close', true);
      if (!ok) return;
    }
    void App.CloseTab(t.id);
    t.view.destroy();
    t.el.remove();
    const i = this.tabs.indexOf(t);
    this.tabs.splice(i, 1);
    if (this.active === t) {
      const next = this.tabs[Math.min(i, this.tabs.length - 1)];
      if (next) this.activate(next);
      else {
        this.active = null;
        this.welcome.hidden = false;
        this.renderTabs();
        this.renderToolbar();
        this.updateStatus();
      }
    } else {
      this.renderTabs(); // closing a background tab: just refresh the tab bar
    }
    this.persist();
  }

  cycleTab(dir: 1 | -1) {
    if (!this.active || this.tabs.length < 2) return;
    const i = this.tabs.indexOf(this.active);
    this.activate(this.tabs[(i + dir + this.tabs.length) % this.tabs.length]);
  }

  private renderTabs() {
    const items = this.tabs.map((t) => {
      const cs = state.conns.get(t.connId);
      const el = h(
        'div',
        {
          class: 'tab' + (t === this.active ? ' active' : '') + (t.running ? ' running' : ''),
          title: t.path || t.title,
          draggable: true,
          style: cs?.profile.color ? { '--tab-color': cs.profile.color } : {},
        },
        t.running ? h('span', { class: 'spinner' }) : null,
        h('span', { class: 'tab-title' }, t.title),
        h(
          'button',
          {
            class: 'tab-close' + (t.dirty ? ' dirty' : ''),
            title: 'Close (⌘W)',
            draggable: 'false',
            // Keep the tab bar from re-rendering under the pointer before the click lands.
            onmousedown: (e: Event) => e.stopPropagation(),
            onclick: (e: Event) => {
              e.stopPropagation();
              void this.closeTab(t);
            },
          },
          icon('close', 12),
        ),
      );
      if (cs?.profile.color) el.style.setProperty('--tab-color', cs.profile.color);
      el.addEventListener('mousedown', (e) => {
        if ((e.target as HTMLElement).closest('.tab-close')) return;
        if (e.button === 1) {
          e.preventDefault();
          void this.closeTab(t);
        } else if (e.button === 0 && t !== this.active) this.activate(t);
      });
      el.addEventListener('dragstart', (e) => e.dataTransfer?.setData('text/x-tab', t.id));
      el.addEventListener('dragover', (e) => e.preventDefault());
      el.addEventListener('drop', (e) => {
        const id = e.dataTransfer?.getData('text/x-tab');
        const from = this.tabs.findIndex((x) => x.id === id);
        const to = this.tabs.indexOf(t);
        if (from >= 0 && from !== to) {
          const [m] = this.tabs.splice(from, 1);
          this.tabs.splice(to, 0, m);
          this.renderTabs();
          this.persist();
        }
      });
      el.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        contextMenu(e.clientX, e.clientY, [
          { label: 'Rename…', action: () => this.renameTab(t) },
          { label: 'Duplicate', action: () => this.newTab({ connId: t.connId, database: t.database, text: t.view.state.doc.toString() }) },
          { separator: true, label: '' },
          { label: 'Close', action: () => void this.closeTab(t) },
          {
            label: 'Close Others',
            action: async () => {
              for (const x of [...this.tabs]) if (x !== t) await this.closeTab(x);
            },
          },
        ]);
      });
      return el;
    });
    const add = h('button', { class: 'icon-btn tab-add', title: 'New query (⌘N)', onclick: () => this.newTab() }, icon('plus'));
    this.tabBar.replaceChildren(...items, add);
    this.tabBar.querySelector('.tab.active')?.scrollIntoView({ block: 'nearest', inline: 'nearest' });
  }

  private async renameTab(t: QueryTab) {
    const name = await promptText('Rename tab', 'Name', t.title);
    if (name) {
      t.title = name;
      this.renderTabs();
      this.persist();
    }
  }

  // ---------- toolbar ----------

  private renderToolbar() {
    const t = this.active;
    const opts: HTMLOptionElement[] = [h('option', { value: '' }, t ? 'Select connection…' : 'No query open')];
    for (const [id, cs] of state.conns) {
      opts.push(h('option', { value: id, selected: t?.connId === id }, cs.profile.name + (cs.status === 'connected' ? '' : ' (disconnected)')));
    }
    opts.push(h('option', { value: '__new' }, 'New connection…'));
    this.connSelect.replaceChildren(...opts);
    this.connSelect.disabled = !t;
    this.toolbar.hidden = !t;

    const cs = t ? state.conns.get(t.connId) : undefined;
    const dbs = new Set(cs?.databases ?? []);
    if (t?.database) dbs.add(t.database);
    this.dbSelect.replaceChildren(
      ...(dbs.size ? [...dbs] : ['']).map((d) => h('option', { value: d, selected: d === t?.database }, d || (cs?.status === 'connecting' ? 'Connecting…' : '—'))),
    );
    this.dbSelect.disabled = !t || !cs || cs.status !== 'connected';
    this.runBtn.disabled = !t || !!t.running;
    this.cancelBtn.disabled = !t?.running;
  }

  private async onConnChange() {
    const t = this.active;
    if (!t) return;
    const v = this.connSelect.value;
    if (v === '__new') {
      this.renderToolbar();
      const r = await connectionDialog();
      if (!r) return;
      this.setConnection(t, r.conn.id);
      return;
    }
    this.setConnection(t, v);
  }

  setConnection(t: QueryTab, connId: string, database?: string) {
    if (t.connId !== connId) void App.CloseTab(t.id);
    t.connId = connId;
    this.applyMode(t);
    const cs = state.conns.get(connId);
    t.database = database ?? (cs?.profile.database || cs?.info?.database || '');
    this.renderToolbar();
    this.renderTabs();
    this.updateStatus();
    this.persist();
    if (connId)
      void ensureConnected(connId).then((ok) => {
        if (!ok) return;
        if (!t.database) t.database = state.conns.get(connId)?.info?.database ?? '';
        this.renderToolbar();
        this.updateStatus();
        this.loadSchema(t);
      });
  }

  private loadSchema(t: QueryTab) {
    if (!t.connId || state.conns.get(t.connId)?.status !== 'connected') return;
    const key = t.connId + ':' + t.database;
    let p = this.schemaCache.get(key);
    if (!p) {
      p = App.CompletionSchema(t.connId, t.database).catch(() => ({}));
      this.schemaCache.set(key, p);
    }
    void p.then((schema) => {
      if (t.connId + ':' + t.database !== key || !this.tabs.includes(t)) return;
      if (this.isMongoTab(t)) setMongoMode(t.view, schema ?? {});
      else setCompletionSchema(t.view, schema ?? {});
    });
  }

  private isMongoTab(t: QueryTab) {
    return state.conns.get(t.connId)?.profile.kind === 'mongodb';
  }

  /** Puts the tab's editor in SQL or MongoDB mode to match its connection. */
  private applyMode(t: QueryTab) {
    const mode = this.isMongoTab(t) ? 'mongo' : 'sql';
    if (t.mode === mode) return;
    t.mode = mode;
    if (mode === 'mongo') setMongoMode(t.view);
    else setSQLMode(t.view);
  }

  /** Opens the insert dialog for a collection (from the explorer or a result). */
  async insertDocument(connId: string, db: string, coll: string) {
    const id = await insertDocumentDialog(connId, db, coll);
    if (id) await infoDialog('Document inserted', `Inserted a document with _id ${id} into ${db}.${coll}.`);
  }

  invalidateSchema(connId: string) {
    for (const k of [...this.schemaCache.keys()]) if (k.startsWith(connId + ':')) this.schemaCache.delete(k);
  }

  // ---------- execution ----------

  async execute(mode: 'auto' | 'current' | 'all') {
    const t = this.active;
    if (!t || t.running) return;
    const doc = t.view.state.doc;
    const sel = t.view.state.selection.main;
    let from = 0;
    let to = doc.length;
    if (mode === 'current') {
      if (!sel.empty) ({ from, to } = sel);
      else ({ from, to } = currentStatement(doc.toString(), sel.head));
    } else if (mode === 'auto' && !sel.empty) {
      ({ from, to } = sel);
    }
    const text = doc.sliceString(from, to);
    if (!text.trim()) return;
    if (!t.connId) {
      const c = this.firstConnected();
      if (c) this.setConnection(t, c);
      else if (!state.conns.size) {
        this.onNewConnection();
        return;
      } else {
        this.connSelect.focus();
        void infoDialog('No connection', 'Choose a connection for this query tab in the toolbar first.');
        return;
      }
    }
    if (mode === 'current' && sel.empty) {
      // Briefly highlight what is being run.
      t.view.dispatch({ selection: { anchor: from, head: to } });
    }
    t.lineOffset = doc.lineAt(from).number - 1;

    // Reset results.
    t.running = true;
    t.runToken = uid();
    t.nextSeq = 0;
    t.pending.clear();
    t.results = [];
    t.messages = [];
    t.summary = undefined;
    t.activeResult = 0;
    t.pane = 'results';
    t.startedAt = Date.now();
    t.resultsVisible = true;
    this.renderResults(t);
    this.layout(t);
    this.renderTabs();
    this.renderToolbar();
    this.startTicker();

    const ok = await ensureConnected(t.connId);
    if (ok) this.loadSchema(t);
    if (!ok) {
      t.running = false;
      t.messages.push({ kind: 'error', text: 'Not connected.', time: Date.now() });
      t.pane = 'messages';
      this.renderResults(t);
      this.renderTabs();
      this.renderToolbar();
      return;
    }
    if (!t.database) t.database = state.conns.get(t.connId)?.info?.database ?? '';
    void App.Execute(t.id, t.runToken, t.connId, t.database, text);
  }

  cancel() {
    const t = this.active;
    if (t?.running) void App.Cancel(t.id);
  }

  private tabFor(d: { tabId: string; runId: string }): QueryTab | undefined {
    const t = this.tabs.find((x) => x.id === d.tabId);
    return t && t.runToken === d.runId ? t : undefined;
  }

  private handlers = new Map<string, (d: any) => void>();

  private onExec(name: string, fn: (d: any) => void) {
    this.handlers.set(name, fn);
    EventsOn(name, (d: any) => this.deliver(name, d));
  }

  /** Applies a run's events in sequence order, buffering any that arrive early. */
  private deliver(name: string, d: any) {
    const t = this.tabFor(d);
    if (!t) return;
    if (typeof d.seq !== 'number') {
      this.handlers.get(name)?.(d);
      return;
    }
    if (d.seq !== t.nextSeq) {
      if (d.seq > t.nextSeq) t.pending.set(d.seq, { name, d });
      return;
    }
    this.handlers.get(name)?.(d);
    t.nextSeq++;
    for (let next = t.pending.get(t.nextSeq); next; next = t.pending.get(t.nextSeq)) {
      t.pending.delete(t.nextSeq);
      if (t.runToken !== d.runId) return; // a new run started meanwhile
      this.handlers.get(next.name)?.(next.d);
      t.nextSeq++;
    }
  }

  private listen() {
    this.onExec('exec:resultset', (d: any) => {
      const t = this.tabFor(d);
      if (!t) return;
      const grid = new DataGrid();
      grid.setColumns(d.columns as GridColumn[]);
      grid.onCellOpen = (v, c) => cellViewer(v, c);
      grid.onContextMenu = (e) => this.gridMenu(e, grid);
      grid.onSelectionChange = (s) => {
        this.selInfo = s;
        this.updateStatus();
      };
      t.results[d.index] = { grid, rowCount: 0, truncated: false, done: false, view: 'table' };
      this.renderResults(t);
    });
    this.onExec('exec:docs', (d: any) => {
      const t = this.tabFor(d);
      const rs = t?.results[d.index];
      if (!t || !rs) return;
      rs.docs = d.info as DocsInfo;
      rs.view = this.mongoView;
      this.renderResults(t);
    });
    this.onExec('exec:rows', (d: any) => {
      const t = this.tabFor(d);
      const rs = t?.results[d.index];
      if (!t || !rs) return;
      rs.grid.append(d.rows as Cell[][]);
      rs.rowCount = rs.grid.rowCount;
      this.scheduleHead(t);
    });
    this.onExec('exec:resultdone', (d: any) => {
      const t = this.tabFor(d);
      const rs = t?.results[d.index];
      if (!t || !rs) return;
      rs.done = true;
      rs.truncated = d.truncated;
      this.scheduleHead(t);
    });
    this.onExec('exec:message', (d: any) => {
      const t = this.tabFor(d);
      if (!t) return;
      const m = d.message as Msg;
      if (m.line) m.line += t.lineOffset;
      t.messages.push(m);
      this.scheduleHead(t);
      if (t.pane === 'messages') this.renderResults(t);
    });
    this.onExec('exec:done', (d: any) => {
      const t = this.tabFor(d);
      if (!t) return;
      const s = d.summary as Summary;
      t.running = false;
      t.summary = s;
      if (s.database && s.database !== t.database) {
        t.database = s.database;
        this.loadSchema(t);
      }
      const n = t.results.filter(Boolean).length;
      if (s.errors > 0 && n === 0) t.pane = 'messages';
      if (n === 0 && s.errors === 0) t.pane = 'messages';
      t.messages.push({
        kind: 'info',
        text: `Completion time: ${new Date().toLocaleString()} · elapsed ${formatDuration(s.elapsedMs)}`,
        time: Date.now(),
      });
      // DDL may have changed objects; refresh autocomplete lazily.
      if (/\b(create|alter|drop|sp_rename|createCollection|insertOne|insertMany)\b/i.test(t.view.state.doc.toString())) {
        this.invalidateSchema(t.connId);
        this.loadSchema(t);
      }
      this.renderResults(t);
      this.renderTabs();
      if (t === this.active) {
        this.renderToolbar();
        this.updateStatus();
      }
    });
  }

  private headTimer = 0;
  private scheduleHead(t: QueryTab) {
    if (this.headTimer) return;
    this.headTimer = window.setTimeout(() => {
      this.headTimer = 0;
      if (t === this.active) {
        this.renderResultsHead(t);
        this.updateStatus();
      }
    }, 100);
  }

  private startTicker() {
    if (this.ticker) return;
    this.ticker = window.setInterval(() => {
      if (!this.tabs.some((t) => t.running)) {
        clearInterval(this.ticker);
        this.ticker = 0;
      }
      this.updateStatus();
    }, 250);
  }

  // ---------- results ----------

  private renderResults(t: QueryTab) {
    this.renderResultsHead(t);
    const body = t.resultsBody;
    if (t.pane === 'messages') {
      const list = h(
        'div',
        { class: 'messages' },
        ...t.messages.map((m) =>
          h(
            'div',
            {
              class: 'msg ' + m.kind + (m.line ? ' clickable' : ''),
              title: m.line ? 'Click to go to line ' + m.line : '',
              onclick: m.line ? () => gotoLine(t.view, m.line!) : null,
            },
            h('span', { class: 'msg-time' }, new Date(m.time).toLocaleTimeString()),
            h('pre', {}, m.text),
          ),
        ),
      );
      if (t.running) list.append(h('div', { class: 'msg info' }, h('span', { class: 'msg-time' }), h('pre', {}, 'Executing…')));
      body.replaceChildren(list);
      list.scrollTop = list.scrollHeight;
      return;
    }
    const rs = t.results[t.activeResult];
    if (rs?.docs && rs.view === 'docs') {
      if (!rs.docsView)
        rs.docsView = new DocsView(t.connId, rs.docs, (msg) => {
          t.messages.push({ kind: 'info', text: msg, time: Date.now() });
          this.renderResultsHead(t);
        });
      body.replaceChildren(rs.docsView.el);
    } else if (rs) body.replaceChildren(rs.grid.el);
    else if (t.running) body.replaceChildren(h('div', { class: 'results-empty' }, h('span', { class: 'spinner' }), ' Executing query…'));
    else if (t.summary) body.replaceChildren(h('div', { class: 'results-empty' }, 'No result sets. See Messages.'));
    else body.replaceChildren(h('div', { class: 'results-empty' }, 'Run a query to see results here.'));
  }

  private renderResultsHead(t: QueryTab) {
    const sets = t.results.map((r, i) => ({ r, i })).filter((x) => x.r);
    const errors = t.messages.filter((m) => m.kind === 'error').length;
    const chips: HTMLElement[] = [];
    if (sets.length <= 1) {
      chips.push(
        h(
          'button',
          { class: 'pill' + (t.pane === 'results' ? ' on' : ''), onclick: () => this.showPane(t, 'results', 0) },
          'Results',
          sets[0] ? h('span', { class: 'count' }, rowsLabel(sets[0].r)) : null,
        ),
      );
    } else {
      for (const { r, i } of sets) {
        chips.push(
          h(
            'button',
            { class: 'pill' + (t.pane === 'results' && t.activeResult === i ? ' on' : ''), onclick: () => this.showPane(t, 'results', i) },
            `Result ${i + 1}`,
            h('span', { class: 'count' }, rowsLabel(r)),
          ),
        );
      }
    }
    chips.push(
      h(
        'button',
        { class: 'pill' + (t.pane === 'messages' ? ' on' : '') + (errors ? ' has-error' : ''), onclick: () => this.showPane(t, 'messages') },
        'Messages',
        errors ? h('span', { class: 'count err' }, String(errors)) : null,
      ),
    );
    const rs = t.results[t.activeResult];
    const canExport = t.pane === 'results' && !!rs && rs.rowCount > 0;
    const docs = t.pane === 'results' ? rs?.docs : undefined;
    const setView = (v: 'docs' | 'table') => {
      if (!rs) return;
      rs.view = v;
      this.mongoView = v;
      this.renderResults(t);
    };
    const actions = h(
      'div',
      { class: 'results-actions' },
      docs
        ? h(
            'div',
            { class: 'segmented small' },
            h('button', { class: 'seg' + (rs!.view === 'docs' ? ' on' : ''), onclick: () => setView('docs') }, 'Documents'),
            h('button', { class: 'seg' + (rs!.view === 'table' ? ' on' : ''), onclick: () => setView('table') }, 'Table'),
          )
        : null,
      docs && rs!.view === 'docs'
        ? h(
            'div',
            { class: 'segmented small' },
            h('button', { class: 'seg', title: 'Expand every node', onclick: () => rs!.docsView?.setAll(true) }, 'Expand all'),
            h('button', { class: 'seg', title: 'Collapse every document to one line', onclick: () => rs!.docsView?.setAll(false) }, 'Collapse all'),
          )
        : null,
      docs?.collection && docs.editable
        ? h(
            'button',
            { class: 'btn small', title: `Insert a document into ${docs.collection}`, onclick: () => void this.insertDocument(t.connId, docs.database, docs.collection) },
            icon('plus', 12),
            'Insert',
          )
        : null,
      docs?.truncated
        ? h('span', { class: 'warn', title: 'Use .limit() and .skip() to page' }, `First ${docs.docs.length.toLocaleString()} documents`)
        : rs?.truncated
          ? h('span', { class: 'warn', title: 'Increase in Settings' }, `Limited to ${state.settings.maxRows.toLocaleString()} rows`)
          : null,
      h('button', { class: 'btn small', disabled: !canExport, title: 'Export as CSV', onclick: () => this.export(rs!, 'csv') }, icon('download', 12), 'CSV'),
      h('button', { class: 'btn small', disabled: !canExport, title: 'Export as JSON', onclick: () => this.export(rs!, 'json') }, icon('download', 12), 'JSON'),
      h(
        'button',
        {
          class: 'btn small',
          disabled: !canExport,
          title: docs && rs!.view === 'docs' ? 'Copy all documents' : 'Copy all rows with headers',
          onclick: () => void App.CopyText(docs && rs!.view === 'docs' ? docs.docs.join('\n\n') : toTSV(rs!.grid)),
        },
        icon('copy', 12),
        'Copy',
      ),
      h('button', { class: 'icon-btn', title: 'Maximize results', onclick: () => this.toggleMaxResults(t) }, icon('maximize', 13)),
    );
    t.resultsHead.replaceChildren(h('div', { class: 'pills' }, ...chips), actions);
  }

  private showPane(t: QueryTab, pane: 'results' | 'messages', idx = t.activeResult) {
    t.pane = pane;
    t.activeResult = idx;
    this.renderResults(t);
  }

  private async export(rs: ResultSet, fmt: 'csv' | 'json') {
    const cols = rs.grid.columns;
    const rows = rs.grid.displayRows();
    const content =
      fmt === 'csv' ? toCSV(cols, rows) : rs.docs ? '[\n' + rs.docs.json.join(',\n') + '\n]\n' : toJSON(cols, rows);
    const name = `${(this.active?.title || 'results').replace(/[^\w.-]+/g, '_')}.${fmt}`;
    try {
      await App.ExportText(name, content);
    } catch (e) {
      showErrorDialog('Export failed', errText(e));
    }
  }

  private gridMenu(e: MouseEvent, grid: DataGrid) {
    const copy = (s: string) => void navigator.clipboard.writeText(s);
    contextMenu(e.clientX, e.clientY, [
      { label: 'Copy', shortcut: '⌘C', action: () => copy(grid.selectionText(false)) },
      { label: 'Copy with Headers', shortcut: '⇧⌘C', action: () => copy(grid.selectionText(true)) },
      { separator: true, label: '' },
      { label: 'Copy as CSV', action: () => { const d = grid.selectionData(); copy(toCSV(d.cols, d.rows)); } },
      { label: 'Copy as JSON', action: () => { const d = grid.selectionData(); copy(toJSON(d.cols, d.rows)); } },
      { label: 'Copy as Markdown', action: () => { const d = grid.selectionData(); copy(toMarkdown(d.cols, d.rows)); } },
      { label: 'Copy as INSERT', action: () => { const d = grid.selectionData(); copy(toInsert(d.cols, d.rows)); } },
      { separator: true, label: '' },
      { label: 'Select All', shortcut: '⌘A', action: () => grid.selectAll() },
    ]);
  }

  // ---------- layout ----------

  private setupSplitter(t: QueryTab) {
    const bar = t.el.querySelector('.hsplit') as HTMLElement;
    bar.addEventListener('mousedown', (e) => {
      e.preventDefault();
      const rect = t.el.getBoundingClientRect();
      document.body.classList.add('resizing-v');
      const move = (ev: MouseEvent) => {
        const frac = 1 - (ev.clientY - rect.top) / rect.height;
        this.resultsFrac = Math.min(0.9, Math.max(0.1, frac));
        t.resultsVisible = true;
        this.layout(t);
      };
      const up = () => {
        document.body.classList.remove('resizing-v');
        window.removeEventListener('mousemove', move);
        window.removeEventListener('mouseup', up);
        try {
          localStorage.setItem('sqlstudio.resultsFrac', String(this.resultsFrac));
        } catch {
          /* ignore */
        }
      };
      window.addEventListener('mousemove', move);
      window.addEventListener('mouseup', up);
    });
    bar.addEventListener('dblclick', () => this.toggleResults());
  }

  private layout(t: QueryTab) {
    const show = t.resultsVisible;
    t.el.style.gridTemplateRows = show ? `minmax(60px, ${1 - this.resultsFrac}fr) 5px minmax(80px, ${this.resultsFrac}fr)` : '1fr 0 0';
    t.resultsEl.hidden = !show;
  }

  toggleResults() {
    const t = this.active;
    if (!t) return;
    t.resultsVisible = !t.resultsVisible;
    this.layout(t);
  }

  private toggleMaxResults(t: QueryTab) {
    this.resultsFrac = this.resultsFrac > 0.85 ? 0.45 : 0.9;
    this.layout(t);
  }

  // ---------- status ----------

  private updateStatus() {
    const t = this.active;
    const cs = t ? state.conns.get(t.connId) : undefined;
    const left: (Node | string)[] = [];
    if (cs) {
      left.push(h('span', { class: 'status-dot ' + cs.status }));
      left.push(h('span', {}, cs.profile.name));
      if (cs.info) {
        const product = cs.profile.kind === 'mongodb' ? cs.info.edition : cs.info.isAzure ? 'Azure SQL' : 'SQL Server';
        left.push(h('span', { class: 'muted' }, `${cs.info.login ? cs.info.login + ' · ' : ''}${product} ${cs.info.version}`));
      }
      if (t?.database) left.push(h('span', { class: 'muted' }, t.database));
    } else left.push(h('span', { class: 'muted' }, t ? 'Not connected' : 'Ready'));
    this.statusLeft.replaceChildren(...left);

    const right: (Node | string)[] = [];
    if (this.selInfo && t?.pane === 'results') right.push(h('span', {}, this.selInfo));
    if (t?.running) right.push(h('span', { class: 'running' }, h('span', { class: 'spinner' }), `Executing… ${formatDuration(Date.now() - t.startedAt)}`));
    else if (t?.summary) {
      const s = t.summary;
      const rows = t.results.reduce((a, r) => a + (r?.rowCount ?? 0), 0);
      right.push(
        h(
          'span',
          { class: s.errors ? 'err' : s.cancelled ? 'warn' : 'ok' },
          s.cancelled ? 'Cancelled' : s.errors ? `Completed with ${s.errors} error${s.errors > 1 ? 's' : ''}` : 'Query succeeded',
        ),
      );
      const noun = t && this.isMongoTab(t) ? 'document' : 'row';
      right.push(h('span', {}, `${rows.toLocaleString()} ${noun}${rows === 1 ? '' : 's'}`));
      right.push(h('span', {}, formatDuration(s.elapsedMs)));
    }
    this.statusRight.replaceChildren(...right);
  }

  // ---------- commands ----------

  async format() {
    const t = this.active;
    if (!t) return;
    try {
      if (this.isMongoTab(t)) await formatMongo(t.view);
      else await formatSQL(t.view);
      t.view.focus();
    } catch (e) {
      if (e instanceof FormatError) gotoLine(t.view, e.line);
      showErrorDialog('Could not format the query', errText(e));
    }
  }

  /** Comment / uncomment / toggle comments on the selected lines. */
  comment(mode: 'add' | 'remove' | 'toggle') {
    const t = this.active;
    if (!t) return;
    if (mode === 'add') commentLines(t.view);
    else if (mode === 'remove') uncommentLines(t.view);
    else toggleCommentLines(t.view);
  }

  /** Checks the selection (or the whole query) without running it. */
  async validate() {
    const t = this.active;
    if (!t || t.running) return;
    const doc = t.view.state.doc;
    const sel = t.view.state.selection.main;
    const from = sel.empty ? 0 : sel.from;
    const text = doc.sliceString(from, sel.empty ? doc.length : sel.to);
    if (!text.trim()) return;
    if (!t.connId) {
      const c = this.firstConnected();
      if (c) this.setConnection(t, c);
      else {
        void infoDialog('No connection', 'Choose a connection for this query tab in the toolbar first.');
        return;
      }
    }
    const offset = doc.lineAt(from).number - 1;
    t.messages = [{ kind: 'info', text: 'Validating…', time: Date.now() }];
    t.pane = 'messages';
    t.resultsVisible = true;
    this.renderResults(t);
    this.layout(t);
    if (!(await ensureConnected(t.connId))) {
      t.messages = [{ kind: 'error', text: 'Not connected.', time: Date.now() }];
      this.renderResults(t);
      return;
    }
    try {
      const msgs = await App.Validate(t.connId, t.database, text);
      t.messages = msgs.map((m) => ({ kind: m.kind, text: m.text, line: m.line ? m.line + offset : undefined, time: m.time }));
    } catch (e) {
      t.messages = [{ kind: 'error', text: errText(e), time: Date.now() }];
    }
    if (t === this.active) this.renderResults(t);
    const firstErr = t.messages.find((m) => m.kind === 'error' && m.line);
    if (firstErr?.line) gotoLine(t.view, firstErr.line);
  }

  async openFile() {
    try {
      const f = await App.OpenFile();
      if (!f) return;
      const existing = this.tabs.find((t) => t.path === f.path);
      if (existing) return this.activate(existing);
      // Replace an empty untitled tab.
      const t = this.active;
      if (t && !t.path && !t.dirty && t.view.state.doc.length === 0) {
        t.view.dispatch({ changes: { from: 0, to: 0, insert: f.content } });
        t.path = f.path;
        t.title = f.name;
        t.dirty = false;
        this.renderTabs();
        this.persist();
        return;
      }
      const nt = this.newTab({ text: f.content, title: f.name, path: f.path });
      nt.dirty = false;
      this.renderTabs();
    } catch (e) {
      showErrorDialog('Could not open file', errText(e));
    }
  }

  async save(saveAs = false) {
    const t = this.active;
    if (!t) return;
    try {
      const name = t.path ? t.path.split('/').pop()! : t.title.replace(/[^\w.-]+/g, '_') + '.sql';
      const p = await App.SaveFile(t.path, name, t.view.state.doc.toString(), saveAs);
      if (!p) return;
      t.path = p;
      t.title = p.split('/').pop() || t.title;
      t.dirty = false;
      this.renderTabs();
      this.persist();
    } catch (e) {
      showErrorDialog('Could not save file', errText(e));
    }
  }

  newTabFor(connId: string, db?: string, text?: string, run?: boolean) {
    const t = this.active;
    let tab: QueryTab;
    // Reuse the current tab if it is empty and untouched.
    if (t && !t.dirty && !t.path && t.view.state.doc.length === 0 && !t.running) {
      tab = t;
      this.setConnection(t, connId, db);
      if (text) t.view.dispatch({ changes: { from: 0, to: 0, insert: text } });
      t.dirty = false;
    } else {
      tab = this.newTab({ connId, database: db, text });
    }
    if (text) tab.dirty = false;
    this.renderTabs();
    if (run) void this.execute('all');
  }

  // ---------- session persistence (restores open tabs on relaunch) ----------

  persist() {
    clearTimeout(this.saveTimer);
    this.saveTimer = window.setTimeout(() => {
      try {
        const data = {
          active: this.active ? this.tabs.indexOf(this.active) : -1,
          counter: this.counter,
          tabs: this.tabs.map((t) => ({
            title: t.title,
            path: t.path,
            connId: t.connId,
            database: t.database,
            dirty: t.dirty,
            text: t.view.state.doc.length < 2_000_000 ? t.view.state.doc.toString() : '',
          })),
        };
        localStorage.setItem(SESSION_KEY, JSON.stringify(data));
      } catch {
        /* storage unavailable */
      }
    }, 400);
  }

  restore(): boolean {
    try {
      const raw = localStorage.getItem(SESSION_KEY);
      if (!raw) return false;
      const data = JSON.parse(raw);
      if (!Array.isArray(data.tabs) || !data.tabs.length) return false;
      this.counter = data.counter || 0;
      for (const s of data.tabs) {
        const t = this.newTab({ connId: s.connId || undefined, database: s.database, text: s.text, title: s.title, path: s.path, lazy: true });
        if (!s.connId) t.connId = '';
        t.dirty = !!s.dirty;
      }
      this.renderTabs();
      const a = this.tabs[data.active] ?? this.tabs[0];
      if (a) this.activate(a);
      return true;
    } catch {
      return false;
    }
  }

  get activeTab() {
    return this.active;
  }
}

function rowsLabel(r: ResultSet): string {
  return r.rowCount.toLocaleString() + (r.truncated ? '+' : '');
}

function toTSV(grid: DataGrid): string {
  const cols = grid.columns;
  const lines = [cols.map((c) => c.name).join('\t')];
  for (const r of grid.displayRows()) lines.push(r.map((v) => (v == null ? 'NULL' : v.replace(/[\t\r\n]+/g, ' '))).join('\t'));
  return lines.join('\n');
}
