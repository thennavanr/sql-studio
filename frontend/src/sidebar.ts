import * as App from './wailsjs/go/main/App';
import type { engine } from './wailsjs/go/models';
import { connectionDialog, confirmDialog, contextMenu, showErrorDialog, type MenuItem } from './dialogs';
import { disconnect, ensureConnected, loadConnections, on, state } from './state';
import { bracket, errText, h } from './util';
import { icon } from './icons';

export interface SidebarActions {
  newQuery: (connId: string, db?: string, text?: string, run?: boolean) => void;
}

type Kind = 'table' | 'view' | 'procedure' | 'function';
const FOLDERS: [Kind, string][] = [
  ['table', 'Tables'],
  ['view', 'Views'],
  ['procedure', 'Stored Procedures'],
  ['function', 'Functions'],
];

interface Loadable<T> {
  loading?: boolean;
  data?: T;
  error?: string;
}

export class Sidebar {
  readonly el: HTMLElement;
  private tree: HTMLElement;
  private filter: HTMLInputElement;
  private expanded = new Set<string>();
  private objects = new Map<string, Loadable<engine.DbObject[]>>();
  private columns = new Map<string, Loadable<engine.ColumnInfo[]>>();
  private dbLists = new Map<string, Loadable<string[]>>();
  private selected = '';

  constructor(private actions: SidebarActions) {
    this.filter = h('input', { class: 'input filter', type: 'search', placeholder: 'Filter objects…', spellcheck: false });
    this.filter.addEventListener('input', () => this.render());
    this.tree = h('div', { class: 'tree', tabIndex: 0 });
    this.el = h(
      'aside',
      { class: 'sidebar' },
      h(
        'div',
        { class: 'side-head' },
        h('span', { class: 'side-title' }, 'CONNECTIONS'),
        h('button', { class: 'icon-btn', title: 'New connection (⇧⌘N)', onclick: () => this.addConnection() }, icon('plus')),
      ),
      h('div', { class: 'side-filter' }, this.filter),
      this.tree,
    );
    on('connections', () => this.render());
  }

  async addConnection() {
    const r = await connectionDialog();
    if (r?.connect) await this.connectAndExpand(r.conn.id);
  }

  async connectAndExpand(id: string) {
    this.expanded.add('c:' + id);
    this.render();
    if (await ensureConnected(id)) {
      const cs = state.conns.get(id)!;
      // Auto-expand the profile's database for a quick start.
      const db = cs.profile.database || cs.info?.database;
      if (db) this.expanded.add(`d:${id}:${db}`);
      this.render();
    }
  }

  private key(...parts: string[]) {
    return parts.join(':');
  }

  private toggle(key: string, after?: () => void) {
    if (this.expanded.has(key)) this.expanded.delete(key);
    else {
      this.expanded.add(key);
      after?.();
    }
    this.render();
  }

  private loadDbList(connId: string, force = false) {
    const cur = this.dbLists.get(connId);
    if (!force && cur && (cur.data || cur.loading)) return;
    const cs = state.conns.get(connId);
    if (!force && cs?.databases) {
      this.dbLists.set(connId, { data: cs.databases });
      return;
    }
    this.dbLists.set(connId, { loading: true });
    App.ListDatabases(connId)
      .then((d) => this.dbLists.set(connId, { data: d }))
      .catch((e) => this.dbLists.set(connId, { error: errText(e) }))
      .finally(() => this.render());
  }

  private loadObjects(connId: string, db: string, force = false) {
    const k = connId + ':' + db;
    const cur = this.objects.get(k);
    if (!force && cur && (cur.data || cur.loading)) return;
    this.objects.set(k, { loading: true });
    App.ListObjects(connId, db)
      .then((d) => this.objects.set(k, { data: d }))
      .catch((e) => this.objects.set(k, { error: errText(e) }))
      .finally(() => this.render());
  }

  private loadColumns(connId: string, db: string, o: engine.DbObject) {
    const k = `${connId}:${db}:${o.schema}.${o.name}`;
    const cur = this.columns.get(k);
    if (cur && (cur.data || cur.loading)) return;
    this.columns.set(k, { loading: true });
    App.ListColumns(connId, db, o.schema, o.name)
      .then((d) => this.columns.set(k, { data: d }))
      .catch((e) => this.columns.set(k, { error: errText(e) }))
      .finally(() => this.render());
  }

  refreshConnection(connId: string) {
    for (const m of [this.objects, this.columns] as Map<string, unknown>[])
      for (const k of [...m.keys()]) if (k.startsWith(connId + ':')) m.delete(k);
    this.dbLists.delete(connId);
    this.loadDbList(connId, true);
    for (const k of this.expanded) {
      const m = /^d:([^:]+):(.*)$/.exec(k);
      if (m && m[1] === connId) this.loadObjects(connId, m[2], true);
    }
    this.render();
  }

  private row(
    depth: number,
    opts: {
      key: string;
      label: string;
      iconName: string;
      expandable?: boolean;
      expanded?: boolean;
      detail?: string;
      color?: string;
      cls?: string;
      title?: string;
      onToggle?: () => void;
      onOpen?: () => void;
      menu?: () => MenuItem[];
      badge?: Node;
    },
  ): HTMLElement {
    const twisty = h('span', { class: 'twisty' + (opts.expandable ? '' : ' leaf') }, opts.expandable ? icon(opts.expanded ? 'chevronDown' : 'chevronRight') : '');
    const el = h(
      'div',
      {
        class: 'node' + (opts.cls ? ' ' + opts.cls : '') + (this.selected === opts.key ? ' selected' : ''),
        style: { paddingLeft: 6 + depth * 14 + 'px' },
        title: opts.title ?? opts.label,
      },
      twisty,
      h('span', { class: 'node-icon', style: opts.color ? { color: opts.color } : {} }, icon(opts.iconName)),
      h('span', { class: 'node-label' }, opts.label),
      opts.detail ? h('span', { class: 'node-detail' }, opts.detail) : null,
      opts.badge ?? null,
    );
    el.addEventListener('click', (e) => {
      this.selected = opts.key;
      if (opts.expandable && (e.target as HTMLElement).closest('.twisty')) opts.onToggle?.();
      else if (opts.expandable && !opts.onOpen) opts.onToggle?.();
      else this.render();
    });
    el.addEventListener('dblclick', (e) => {
      if ((e.target as HTMLElement).closest('.twisty')) return;
      if (opts.onOpen) opts.onOpen();
      else if (opts.expandable) opts.onToggle?.();
    });
    if (opts.menu) {
      el.addEventListener('contextmenu', (e) => {
        e.preventDefault();
        this.selected = opts.key;
        this.render();
        contextMenu(e.clientX, e.clientY, opts.menu!());
      });
    }
    return el;
  }

  private status(depth: number, text: string, error = false): HTMLElement {
    return h('div', { class: 'node status' + (error ? ' error' : ''), style: { paddingLeft: 6 + depth * 14 + 20 + 'px' }, title: text }, text);
  }

  render() {
    const scroll = this.tree.scrollTop;
    const out: HTMLElement[] = [];
    const filter = this.filter.value.trim().toLowerCase();
    const conns = [...state.conns.values()];
    if (!conns.length) {
      out.push(
        h(
          'div',
          { class: 'empty-side' },
          h('p', {}, 'No connections yet.'),
          h('button', { class: 'btn primary', onclick: () => this.addConnection() }, 'New Connection'),
        ),
      );
    }
    let lastGroup: string | undefined;
    for (const cs of conns) {
      const p = cs.profile;
      if ((p.group || '') !== (lastGroup ?? '')) {
        if (p.group) out.push(h('div', { class: 'group-label' }, p.group));
        lastGroup = p.group || '';
      }
      const ck = this.key('c', p.id);
      const open = this.expanded.has(ck);
      const dot = h('span', { class: 'status-dot ' + cs.status, title: cs.status });
      out.push(
        this.row(0, {
          key: ck,
          label: p.name,
          iconName: 'server',
          color: p.color || undefined,
          expandable: true,
          expanded: open,
          cls: 'conn',
          title: `${p.name}\n${p.server}${cs.info ? `\n${cs.info.edition} ${cs.info.version}\nLogged in as ${cs.info.login}` : ''}`,
          badge: dot,
          onToggle: () => {
            if (open) this.toggle(ck);
            else void this.connectAndExpand(p.id);
          },
          menu: () => this.connMenu(p.id),
        }),
      );
      if (!open) continue;
      if (cs.status === 'connecting') {
        out.push(this.status(1, p.auth === 'sql' ? 'Connecting…' : 'Signing in… (check your browser)'));
        continue;
      }
      if (cs.status === 'error') {
        out.push(this.status(1, cs.error ?? 'Connection failed', true));
        continue;
      }
      if (cs.status !== 'connected') continue;
      this.loadDbList(p.id);
      const dbl = this.dbLists.get(p.id);
      if (dbl?.loading) out.push(this.status(1, 'Loading…'));
      if (dbl?.error) out.push(this.status(1, dbl.error, true));
      for (const db of dbl?.data ?? []) this.renderDb(out, p.id, db, filter);
    }
    this.tree.replaceChildren(...out);
    this.tree.scrollTop = scroll;
  }

  private renderDb(out: HTMLElement[], connId: string, db: string, filter: string) {
    const dk = this.key('d', connId, db);
    const open = this.expanded.has(dk);
    out.push(
      this.row(1, {
        key: dk,
        label: db,
        iconName: 'database',
        expandable: true,
        expanded: open,
        onToggle: () => this.toggle(dk, () => this.loadObjects(connId, db)),
        menu: () => [
          { label: 'New Query', action: () => this.actions.newQuery(connId, db) },
          { label: 'Refresh', action: () => this.loadObjects(connId, db, true) },
          { separator: true, label: '' },
          { label: 'Copy Name', action: () => void navigator.clipboard.writeText(db) },
        ],
      }),
    );
    if (!open) return;
    this.loadObjects(connId, db);
    const objs = this.objects.get(connId + ':' + db);
    if (objs?.loading) {
      out.push(this.status(2, 'Loading…'));
      return;
    }
    if (objs?.error) {
      out.push(this.status(2, objs.error, true));
      return;
    }
    for (const [kind, label] of FOLDERS) {
      let items = (objs?.data ?? []).filter((o) => o.kind === kind);
      if (filter) items = items.filter((o) => `${o.schema}.${o.name}`.toLowerCase().includes(filter));
      const fk = this.key('f', connId, db, kind);
      const fopen = this.expanded.has(fk) || (!!filter && items.length > 0);
      if (filter && !items.length) continue;
      out.push(
        this.row(2, {
          key: fk,
          label,
          iconName: 'folder',
          detail: String(items.length),
          expandable: true,
          expanded: fopen,
          onToggle: () => this.toggle(fk),
        }),
      );
      if (!fopen) continue;
      const max = 2000;
      for (const o of items.slice(0, max)) this.renderObject(out, connId, db, o);
      if (items.length > max) out.push(this.status(3, `…and ${items.length - max} more. Use the filter box.`));
    }
  }

  private renderObject(out: HTMLElement[], connId: string, db: string, o: engine.DbObject) {
    const ok = this.key('o', connId, db, o.schema, o.name);
    const hasCols = o.kind === 'table' || o.kind === 'view';
    const open = this.expanded.has(ok);
    const full = `${bracket(o.schema)}.${bracket(o.name)}`;
    out.push(
      this.row(3, {
        key: ok,
        label: `${o.schema}.${o.name}`,
        iconName: o.kind,
        expandable: hasCols,
        expanded: open,
        onToggle: () => this.toggle(ok, () => this.loadColumns(connId, db, o)),
        onOpen: hasCols ? () => this.selectTop(connId, db, o) : () => this.script(connId, db, o),
        menu: () => {
          const items: MenuItem[] = [];
          if (hasCols) {
            items.push({ label: 'Select Top 1000', action: () => this.selectTop(connId, db, o) });
            items.push({
              label: 'Count Rows',
              action: () => this.actions.newQuery(connId, db, `SELECT COUNT_BIG(*) AS row_count FROM ${full};`, true),
            });
          }
          if (o.kind === 'procedure') items.push({ label: 'Script as EXECUTE', action: () => this.scriptExec(connId, db, o) });
          items.push({ label: 'Script as CREATE', action: () => this.script(connId, db, o) });
          if (o.kind !== 'table') items.push({ label: 'Script as ALTER', action: () => this.script(connId, db, o, true) });
          items.push({ separator: true, label: '' });
          items.push({ label: 'Copy Name', action: () => void navigator.clipboard.writeText(full) });
          return items;
        },
      }),
    );
    if (!open || !hasCols) return;
    const cols = this.columns.get(`${connId}:${db}:${o.schema}.${o.name}`);
    if (cols?.loading) out.push(this.status(4, 'Loading…'));
    if (cols?.error) out.push(this.status(4, cols.error, true));
    for (const c of cols?.data ?? []) {
      out.push(
        this.row(4, {
          key: `${ok}:${c.name}`,
          label: c.name,
          iconName: c.pk ? 'key' : 'column',
          detail: `${c.type}${c.nullable ? ', null' : ', not null'}${c.identity ? ', identity' : ''}${c.computed ? ', computed' : ''}`,
          cls: 'col',
          menu: () => [{ label: 'Copy Name', action: () => void navigator.clipboard.writeText(bracket(c.name)) }],
        }),
      );
    }
  }

  private selectTop(connId: string, db: string, o: engine.DbObject) {
    this.actions.newQuery(connId, db, `SELECT TOP (1000) *\nFROM ${bracket(o.schema)}.${bracket(o.name)};\n`, true);
  }

  private async script(connId: string, db: string, o: engine.DbObject, alter = false) {
    try {
      let s = await App.ScriptObject(connId, db, o.schema, o.name, o.kind);
      if (alter) s = s.replace(/\bCREATE(\s+OR\s+ALTER)?(\s+)(PROCEDURE|PROC|VIEW|FUNCTION|TRIGGER)\b/i, 'ALTER$2$3');
      this.actions.newQuery(connId, db, s);
    } catch (e) {
      showErrorDialog('Could not script object', errText(e));
    }
  }

  private async scriptExec(connId: string, db: string, o: engine.DbObject) {
    this.actions.newQuery(connId, db, `EXEC ${bracket(o.schema)}.${bracket(o.name)}\n    -- @param = value\n;\n`);
  }

  private connMenu(id: string): MenuItem[] {
    const cs = state.conns.get(id)!;
    const connected = cs.status === 'connected';
    return [
      { label: 'New Query', action: () => this.actions.newQuery(id) },
      { separator: true, label: '' },
      connected ? { label: 'Refresh', action: () => this.refreshConnection(id) } : { label: 'Connect', action: () => void this.connectAndExpand(id) },
      { label: 'Disconnect', disabled: !connected, action: () => void disconnect(id) },
      { separator: true, label: '' },
      {
        label: 'Edit Connection…',
        action: async () => {
          const r = await connectionDialog(cs.profile);
          if (r) {
            const st = state.conns.get(id);
            if (st) st.status = 'disconnected';
            this.refreshConnection(id);
            if (r.connect) void this.connectAndExpand(id);
          }
        },
      },
      {
        label: 'Duplicate',
        action: async () => {
          const copy = { ...cs.profile, id: '', name: cs.profile.name + ' (copy)' };
          await App.SaveConnection(copy as any);
          await loadConnections();
        },
      },
      {
        label: 'Delete',
        danger: true,
        action: async () => {
          if (await confirmDialog('Delete connection', `Delete “${cs.profile.name}”? Saved credentials are removed from the Keychain.`, 'Delete', true)) {
            await App.DeleteConnection(id);
            await loadConnections();
          }
        },
      },
    ];
  }
}
