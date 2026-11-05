import * as App from './wailsjs/go/main/App';
import { engine, store } from './wailsjs/go/models';
import { applyTheme, emit, loadConnections, state, type Connection } from './state';
import { bracket, errText, formatDuration, h } from './util';
import type { Cell, GridColumn } from './grid';

// ---------- generic modal ----------

export interface ModalButton {
  label: string;
  primary?: boolean;
  danger?: boolean;
  value: string;
}

export function modal(
  title: string,
  body: Node,
  buttons: ModalButton[],
  opts: { wide?: boolean; onButton?: (v: string) => boolean | Promise<boolean>; footerLeft?: Node } = {},
): Promise<string | null> {
  return new Promise((resolve) => {
    const dlg = h('dialog', { class: 'modal' + (opts.wide ? ' wide' : '') });
    const close = (v: string | null) => {
      dlg.close();
      dlg.remove();
      resolve(v);
    };
    const btns = buttons.map((b) =>
      h(
        'button',
        {
          class: 'btn' + (b.primary ? ' primary' : '') + (b.danger ? ' danger' : ''),
          type: b.primary ? 'submit' : 'button',
          onclick: async (e: Event) => {
            e.preventDefault();
            if (opts.onButton) {
              const el = e.currentTarget as HTMLButtonElement;
              el.disabled = true;
              try {
                if (!(await opts.onButton(b.value))) return;
              } finally {
                el.disabled = false;
              }
            }
            close(b.value);
          },
        },
        b.label,
      ),
    );
    const form = h(
      'form',
      { method: 'dialog' },
      h('div', { class: 'modal-head' }, h('h2', {}, title)),
      h('div', { class: 'modal-body' }, body),
      h('div', { class: 'modal-foot' }, opts.footerLeft ?? h('span'), h('div', { class: 'modal-btns' }, ...btns)),
    );
    form.addEventListener('submit', (e) => {
      e.preventDefault();
      const primary = btns.find((b) => b.type === 'submit');
      primary?.click();
    });
    dlg.appendChild(form);
    dlg.addEventListener('cancel', (e) => {
      e.preventDefault();
      close(null);
    });
    document.body.appendChild(dlg);
    dlg.showModal();
    const first = dlg.querySelector<HTMLElement>('input:not([type=checkbox]),select,textarea');
    first?.focus();
  });
}

export function showErrorDialog(title: string, msg: string) {
  void modal(title, h('pre', { class: 'error-text' }, msg), [{ label: 'OK', value: 'ok', primary: true }]);
}

export async function infoDialog(title: string, msg: string) {
  await modal(title, h('p', {}, msg), [{ label: 'OK', value: 'ok', primary: true }]);
}

export async function confirmDialog(title: string, msg: string, okLabel = 'OK', danger = false): Promise<boolean> {
  const r = await modal(title, h('p', {}, msg), [
    { label: 'Cancel', value: 'cancel' },
    { label: okLabel, value: 'ok', primary: true, danger },
  ]);
  return r === 'ok';
}

export async function promptText(title: string, label: string, value = '', password = false): Promise<string | null> {
  const input = h('input', { type: password ? 'password' : 'text', value, class: 'input' });
  const r = await modal(title, h('label', { class: 'field' }, h('span', {}, label), input), [
    { label: 'Cancel', value: 'cancel' },
    { label: 'OK', value: 'ok', primary: true },
  ]);
  return r === 'ok' ? input.value : null;
}

export async function promptPasswordFor(c: Connection): Promise<string | null> {
  const secretLabel = c.auth === 'entraServicePrincipal' ? 'Client secret' : 'Password';
  const input = h('input', { type: 'password', class: 'input', autocomplete: 'off' });
  const remember = h('input', { type: 'checkbox' });
  const r = await modal(
    `Connect to ${c.name}`,
    h(
      'div',
      { class: 'form' },
      h('p', { class: 'muted' }, `${c.user || ''} @ ${c.server}`),
      h('label', { class: 'field' }, h('span', {}, secretLabel), input),
      h('label', { class: 'check' }, remember, ' Remember in Keychain'),
    ),
    [
      { label: 'Cancel', value: 'cancel' },
      { label: 'Connect', value: 'ok', primary: true },
    ],
  );
  if (r !== 'ok') return null;
  if (remember.checked && input.value) {
    await App.SaveConnection(store.Connection.createFrom({ ...c, savePassword: true, password: input.value }));
    await loadConnections();
  }
  return input.value;
}

// ---------- connection dialog ----------

const AUTH_OPTIONS: [string, string][] = [
  ['entraInteractive', 'Microsoft Entra ID – Universal with MFA (browser sign-in)'],
  ['entraAzCli', 'Microsoft Entra ID – Azure CLI (az login)'],
  ['entraDefault', 'Microsoft Entra ID – Default credential chain'],
  ['entraDeviceCode', 'Microsoft Entra ID – Device code'],
  ['entraServicePrincipal', 'Microsoft Entra ID – Service principal (client secret)'],
  ['sql', 'SQL Server authentication'],
];

const COLORS = ['', '#e5484d', '#f76b15', '#ffc53d', '#46a758', '#0090ff', '#8e4ec6', '#d6409f'];

/** Parses ADO.NET / ODBC style connection strings as copied from the Azure portal. */
export function parseConnectionString(s: string): Partial<Connection> {
  const out: Partial<Connection> = {};
  for (const part of s.split(';')) {
    const i = part.indexOf('=');
    if (i < 0) continue;
    const k = part.slice(0, i).trim().toLowerCase().replace(/\s+/g, ' ');
    let v = part.slice(i + 1).trim();
    if (/^\{.*\}$/.test(v)) v = v.slice(1, -1);
    switch (k) {
      case 'server':
      case 'data source':
      case 'address':
      case 'addr':
        out.server = v;
        break;
      case 'initial catalog':
      case 'database':
        out.database = v;
        break;
      case 'user id':
      case 'uid':
      case 'user':
        out.user = v;
        break;
      case 'password':
      case 'pwd':
        out.password = v;
        break;
      case 'encrypt':
        out.encrypt = /^(true|yes|mandatory)$/i.test(v) ? 'true' : /strict/i.test(v) ? 'strict' : 'false';
        break;
      case 'trustservercertificate':
      case 'trust server certificate':
        out.trustServerCertificate = /^(true|yes)$/i.test(v);
        break;
      case 'connection timeout':
      case 'connect timeout':
        out.connectTimeout = Number(v) || 0;
        break;
      case 'authentication': {
        const a = v.toLowerCase().replace(/[\s_-]/g, '');
        if (a.includes('interactive')) out.auth = 'entraInteractive';
        else if (a.includes('serviceprincipal')) out.auth = 'entraServicePrincipal';
        else if (a.includes('devicecode')) out.auth = 'entraDeviceCode';
        else if (a.includes('default')) out.auth = 'entraDefault';
        else if (a.includes('sqlpassword')) out.auth = 'sql';
        else if (a.startsWith('activedirectory')) out.auth = 'entraInteractive';
        break;
      }
    }
  }
  if (!out.auth && out.user && out.password) out.auth = 'sql';
  return out;
}

export async function connectionDialog(existing?: Connection): Promise<{ conn: Connection; connect: boolean } | null> {
  const isNew = !existing;
  const c: Connection = store.Connection.createFrom(
    existing ?? { id: '', name: '', server: '', auth: 'entraInteractive', encrypt: 'true', savePassword: true },
  );
  const hasSaved = existing ? await App.HasSavedPassword(existing.id) : false;

  const inp = (value: any, attrs: Record<string, any> = {}) => h('input', { class: 'input', value: value ?? '', ...attrs });
  const f = {
    server: inp(c.server, { placeholder: 'myserver.database.windows.net', spellcheck: 'false', autocapitalize: 'off' }),
    auth: h('select', { class: 'input' }, ...AUTH_OPTIONS.map(([v, l]) => h('option', { value: v, selected: v === c.auth }, l))),
    user: inp(c.user, { spellcheck: 'false', autocapitalize: 'off' }),
    password: inp('', { type: 'password', placeholder: hasSaved ? '•••••••• (saved in Keychain)' : '' }),
    savePassword: h('input', { type: 'checkbox', checked: c.savePassword !== false }),
    tenantId: inp(c.tenantId, { placeholder: 'optional – e.g. contoso.onmicrosoft.com or GUID', spellcheck: 'false' }),
    database: inp(c.database, { placeholder: 'default', spellcheck: 'false', list: 'db-list' }),
    name: inp(c.name, { placeholder: 'defaults to server / database' }),
    group: inp(c.group, { placeholder: 'e.g. Production' }),
    encrypt: h(
      'select',
      { class: 'input' },
      h('option', { value: 'true', selected: c.encrypt !== 'strict' && c.encrypt !== 'false' }, 'Mandatory (recommended)'),
      h('option', { value: 'strict', selected: c.encrypt === 'strict' }, 'Strict (TDS 8.0)'),
      h('option', { value: 'false', selected: c.encrypt === 'false' }, 'Optional'),
    ),
    trust: h('input', { type: 'checkbox', checked: !!c.trustServerCertificate }),
    port: inp(c.port || '', { type: 'number', placeholder: '1433', min: '1', max: '65535' }),
    timeout: inp(c.connectTimeout || '', { type: 'number', placeholder: '30', min: '1' }),
    readOnly: h('input', { type: 'checkbox', checked: c.applicationIntent === 'ReadOnly' }),
    clientId: inp(c.clientId, { placeholder: 'optional – custom Entra app (public client) ID', spellcheck: 'false' }),
  };
  const dbList = h('datalist', { id: 'db-list' });
  let color = c.color || '';
  const swatches = h(
    'div',
    { class: 'swatches' },
    ...COLORS.map((col) =>
      h('button', {
        type: 'button',
        class: 'swatch' + (col === color ? ' on' : '') + (col ? '' : ' none'),
        style: col ? { background: col } : {},
        title: col || 'No color',
        onclick: (e: Event) => {
          color = col;
          swatches.querySelectorAll('.swatch').forEach((s) => s.classList.remove('on'));
          (e.currentTarget as HTMLElement).classList.add('on');
        },
      }),
    ),
  );

  const userLabel = h('span', {});
  const passLabel = h('span', {});
  const userRow = h('label', { class: 'field' }, userLabel, f.user);
  const passRow = h('div', { class: 'field' }, h('label', {}, passLabel), f.password, h('label', { class: 'check' }, f.savePassword, ' Remember in Keychain'));
  const tenantRow = h('label', { class: 'field' }, h('span', {}, 'Tenant ID'), f.tenantId);
  const clientRow = h('label', { class: 'field' }, h('span', {}, 'Client app ID'), f.clientId);
  const hint = h('p', { class: 'hint' });
  const status = h('div', { class: 'test-status' });

  let kind: 'sqlserver' | 'mongodb' = c.kind === 'mongodb' ? 'mongodb' : 'sqlserver';
  const mf = {
    uri: inp(c.uri, { placeholder: 'mongodb+srv://user@cluster0.example.mongodb.net/', spellcheck: 'false', autocapitalize: 'off' }),
    password: inp('', { type: 'password', placeholder: hasSaved ? '•••••••• (saved in Keychain)' : 'leave empty if the connection string has no user' }),
    savePassword: h('input', { type: 'checkbox', checked: c.savePassword !== false }),
    database: inp(c.database, { placeholder: 'optional – e.g. shop', spellcheck: 'false', autocapitalize: 'off' }),
  };

  const update = () => {
    const a = f.auth.value;
    const entra = a !== 'sql';
    userRow.hidden = a === 'entraAzCli' || a === 'entraDefault';
    passRow.hidden = !(a === 'sql' || a === 'entraServicePrincipal');
    tenantRow.hidden = !entra;
    clientRow.hidden = !(a === 'entraInteractive' || a === 'entraDeviceCode');
    userLabel.textContent = a === 'sql' ? 'Login' : a === 'entraServicePrincipal' ? 'Client (application) ID' : 'Account (optional login hint)';
    f.user.placeholder = a === 'sql' ? 'sa' : a === 'entraServicePrincipal' ? '00000000-0000-0000-0000-000000000000' : 'you@company.com';
    passLabel.textContent = a === 'entraServicePrincipal' ? 'Client secret' : 'Password';
    f.tenantId.placeholder = a === 'entraServicePrincipal' ? 'required – directory (tenant) ID' : 'optional – e.g. contoso.onmicrosoft.com or GUID';
    hint.textContent =
      {
        entraInteractive: 'Opens your browser to sign in (supports MFA). The token is reused for all databases on this connection until you quit.',
        entraAzCli: 'Uses the account from `az login` in Terminal. Install with `brew install azure-cli`.',
        entraDefault: 'Tries environment variables, workload identity, managed identity, Azure CLI and Azure Developer CLI in order.',
        entraDeviceCode: 'Shows a code to enter at microsoft.com/devicelogin – useful when the browser flow is blocked.',
        entraServicePrincipal: 'Signs in as an app registration. The secret is stored in your macOS Keychain.',
        sql: 'Username and password stored in the server. The password is stored in your macOS Keychain.',
      }[a] ?? '';
  };
  f.auth.addEventListener('change', update);
  update();

  const pasteLink = h('button', {
    type: 'button',
    class: 'link',
    onclick: async () => {
      const s = await promptText('Paste connection string', 'ADO.NET / ODBC connection string (e.g. from Azure portal → Connection strings)');
      if (!s) return;
      const p = parseConnectionString(s);
      if (p.server) f.server.value = p.server;
      if (p.database) f.database.value = p.database;
      if (p.user && !/\{your/i.test(p.user)) f.user.value = p.user;
      if (p.password && !/\{your/i.test(p.password)) f.password.value = p.password;
      if (p.auth) f.auth.value = p.auth;
      if (p.encrypt) f.encrypt.value = p.encrypt;
      if (p.trustServerCertificate != null) f.trust.checked = p.trustServerCertificate;
      if (p.connectTimeout) f.timeout.value = String(p.connectTimeout);
      update();
    },
  }, 'Paste connection string…');

  const collect = (): Connection =>
    kind === 'mongodb'
      ? store.Connection.createFrom({
          ...c,
          kind: 'mongodb',
          uri: mf.uri.value.trim(),
          password: mf.password.value,
          savePassword: mf.savePassword.checked,
          database: mf.database.value.trim(),
          name: f.name.value.trim(),
          group: f.group.value.trim(),
          server: '',
          auth: '',
          color,
        })
      : store.Connection.createFrom({
      ...c,
      kind: '',
      uri: '',
      server: f.server.value.trim(),
      auth: f.auth.value,
      user: userRow.hidden ? '' : f.user.value.trim(),
      password: passRow.hidden ? '' : f.password.value,
      savePassword: f.savePassword.checked,
      tenantId: tenantRow.hidden ? '' : f.tenantId.value.trim(),
      clientId: clientRow.hidden ? '' : f.clientId.value.trim(),
      database: f.database.value.trim(),
      name: f.name.value.trim(),
      group: f.group.value.trim(),
      encrypt: f.encrypt.value,
      trustServerCertificate: f.trust.checked,
      port: Number(f.port.value) || 0,
      connectTimeout: Number(f.timeout.value) || 0,
      applicationIntent: f.readOnly.checked ? 'ReadOnly' : '',
      color,
    });

  const sqlSection = h(
    'div',
    { class: 'form' },
    h('label', { class: 'field' }, h('span', {}, 'Server'), f.server),
    h('label', { class: 'field' }, h('span', {}, 'Authentication'), f.auth),
    hint,
    userRow,
    passRow,
    tenantRow,
    h('label', { class: 'field' }, h('span', {}, 'Database'), f.database),
    dbList,
  );
  const mongoSection = h(
    'div',
    { class: 'form' },
    h('label', { class: 'field' }, h('span', {}, 'Connection string'), mf.uri),
    h(
      'p',
      { class: 'hint' },
      'From Atlas (Connect → Drivers), Azure Cosmos DB for MongoDB (Connection strings) or your server, e.g. mongodb://localhost:27017. ' +
        'A password inside the connection string is moved to your Keychain and removed from the saved string.',
    ),
    h(
      'div',
      { class: 'field' },
      h('label', {}, 'Password'),
      mf.password,
      h('label', { class: 'check' }, mf.savePassword, ' Remember in Keychain'),
    ),
    h('label', { class: 'field' }, h('span', {}, 'Default database'), mf.database),
  );
  const sqlAdvanced = h(
    'details',
    { class: 'advanced' },
    h('summary', {}, 'Advanced'),
    h('div', { class: 'grid2' }, h('label', { class: 'field' }, h('span', {}, 'Encrypt'), f.encrypt), h('label', { class: 'field' }, h('span', {}, 'Port'), f.port)),
    h('div', { class: 'grid2' }, h('label', { class: 'field' }, h('span', {}, 'Connect timeout (s)'), f.timeout), clientRow),
    h('label', { class: 'check' }, f.trust, ' Trust server certificate (local / dev servers only)'),
    h('label', { class: 'check' }, f.readOnly, ' Read-only intent (route to readable secondary)'),
  );
  const kindButtons = (['sqlserver', 'mongodb'] as const).map((k) =>
    h(
      'button',
      {
        type: 'button',
        class: 'seg' + (kind === k ? ' on' : ''),
        onclick: () => {
          kind = k;
          applyKind();
          (k === 'mongodb' ? mf.uri : f.server).focus();
        },
      },
      k === 'sqlserver' ? 'SQL Server / Azure SQL' : 'MongoDB',
    ),
  );
  const applyKind = () => {
    kindButtons.forEach((b, i) => b.classList.toggle('on', (i === 0 ? 'sqlserver' : 'mongodb') === kind));
    sqlSection.hidden = kind !== 'sqlserver';
    sqlAdvanced.hidden = kind !== 'sqlserver';
    pasteLink.hidden = kind !== 'sqlserver';
    mongoSection.hidden = kind !== 'mongodb';
    status.textContent = '';
  };
  const missing = (): string => {
    if (kind === 'mongodb') return mf.uri.value.trim() ? '' : 'Connection string is required.';
    return f.server.value.trim() ? '' : 'Server is required.';
  };

  const body = h(
    'div',
    { class: 'form conn-form' },
    h('div', { class: 'segmented' }, ...kindButtons),
    h('div', { class: 'row-between' }, h('span', { class: 'section' }, 'Connection'), pasteLink),
    sqlSection,
    mongoSection,
    h('div', { class: 'grid2' }, h('label', { class: 'field' }, h('span', {}, 'Name'), f.name), h('label', { class: 'field' }, h('span', {}, 'Group'), f.group)),
    h('div', { class: 'field' }, h('span', {}, 'Color'), swatches),
    sqlAdvanced,
    status,
  );
  applyKind();

  const result = await modal(
    isNew ? 'New Connection' : 'Edit Connection',
    body,
    [
      { label: 'Cancel', value: 'cancel' },
      { label: 'Save', value: 'save' },
      { label: 'Save & Connect', value: 'connect', primary: true },
    ],
    {
      wide: true,
      footerLeft: h('button', {
        type: 'button',
        class: 'btn',
        onclick: async (e: Event) => {
          const btn = e.currentTarget as HTMLButtonElement;
          const conn = collect();
          const miss = missing();
          if (miss) {
            status.className = 'test-status err';
            status.textContent = miss;
            return;
          }
          btn.disabled = true;
          status.className = 'test-status';
          status.textContent = 'Connecting…';
          const t0 = performance.now();
          try {
            const info = await App.TestConnection(conn);
            status.className = 'test-status ok';
            status.textContent = `✓ Connected in ${formatDuration(Math.round(performance.now() - t0))} — ${info.edition} ${info.version}${info.login ? ' as ' + info.login : ''}`;
          } catch (err) {
            status.className = 'test-status err';
            status.textContent = errText(err);
          } finally {
            btn.disabled = false;
          }
        },
      }, 'Test Connection'),
      onButton: async (v) => {
        if (v === 'cancel') return true;
        const miss = missing();
        if (miss) {
          status.className = 'test-status err';
          status.textContent = miss;
          (kind === 'mongodb' ? mf.uri : f.server).focus();
          return false;
        }
        return true;
      },
    },
  );
  if (result !== 'save' && result !== 'connect') return null;
  try {
    const saved = await App.SaveConnection(collect());
    await loadConnections();
    return { conn: saved, connect: result === 'connect' };
  } catch (e) {
    showErrorDialog('Could not save connection', errText(e));
    return null;
  }
}

// ---------- script data as INSERT ----------

export interface ScriptDataTarget {
  connId: string;
  database: string;
  schema: string;
  name: string;
  kind: string;
}

/**
 * Asks for options, generates the INSERT script and delivers it. Resolves with
 * the script when the user picked "Open in New Query", otherwise null.
 */
export async function scriptDataDialog(o: ScriptDataTarget): Promise<string | null> {
  let cols: engine.ColumnInfo[] = [];
  try {
    cols = await App.ListColumns(o.connId, o.database, o.schema, o.name);
  } catch {
    /* the script call will report the real error */
  }
  const hasIdentity = o.kind === 'table' && cols.some((c) => c.identity);
  const pk = cols.filter((c) => c.pk).map((c) => bracket(c.name));
  const skipped = cols.filter((c) => c.computed || c.type === 'timestamp' || c.type === 'rowversion').map((c) => c.name);

  const inp = (attrs: Record<string, any>) => h('input', { class: 'input', spellcheck: false, autocapitalize: 'off', ...attrs });
  const target = inp({ value: `${bracket(o.schema)}.${bracket(o.name)}` });
  const allRows = h('input', { type: 'checkbox' });
  const top = inp({ type: 'number', min: '1', value: '1000' });
  allRows.addEventListener('change', () => (top.disabled = allRows.checked));
  const where = h('textarea', {
    class: 'input code',
    rows: 3,
    spellcheck: false,
    placeholder: "optional, e.g.  CreatedAt >= '2024-01-01' AND Status = 'Active'",
  });
  const orderBy = inp({ placeholder: pk.length ? `${pk.join(', ')} (primary key)` : 'optional, e.g. Id DESC' });
  const perInsert = h(
    'select',
    { class: 'input' },
    h('option', { value: '1' }, '1 (one INSERT per row)'),
    h('option', { value: '100', selected: true }, '100'),
    h('option', { value: '1000' }, '1000 (maximum)'),
  );
  const identity = h('input', { type: 'checkbox', checked: hasIdentity });
  const create = h('input', { type: 'checkbox' });
  const status = h('div', { class: 'test-status' });

  const body = h(
    'div',
    { class: 'form' },
    h('p', { class: 'muted' }, `Generates INSERT statements with the data in ${o.database}.${o.schema}.${o.name}.`),
    h('label', { class: 'field' }, h('span', {}, 'Insert into table'), target),
    h(
      'div',
      { class: 'grid2' },
      h('label', { class: 'field' }, h('span', {}, 'Top rows'), top),
      h('label', { class: 'field' }, h('span', {}, 'Rows per INSERT statement'), perInsert),
    ),
    h('label', { class: 'check' }, allRows, ' All rows (up to 200,000)'),
    h('label', { class: 'field' }, h('span', {}, 'Filter (WHERE)'), where),
    h('label', { class: 'field' }, h('span', {}, 'Order by'), orderBy),
    hasIdentity ? h('label', { class: 'check' }, identity, ' Keep identity values (SET IDENTITY_INSERT ON)') : null,
    h('label', { class: 'check' }, create, ' Include CREATE TABLE for the target table'),
    skipped.length ? h('p', { class: 'hint' }, `Skipped (generated by the server): ${skipped.join(', ')}`) : null,
    status,
  );

  let opened: string | null = null;
  await modal(
    'Script Data as INSERT',
    body,
    [
      { label: 'Close', value: 'cancel' },
      { label: 'Copy to Clipboard', value: 'copy' },
      { label: 'Save to File…', value: 'file' },
      { label: 'Open in New Query', value: 'tab', primary: true },
    ],
    {
      wide: true,
      onButton: async (v) => {
        if (v === 'cancel') return true;
        const topN = Number(top.value);
        if (!allRows.checked && (!Number.isInteger(topN) || topN < 1)) {
          status.className = 'test-status err';
          status.textContent = 'Top rows must be a whole number of at least 1, or tick “All rows”.';
          top.focus();
          return false;
        }
        status.className = 'test-status';
        status.textContent = 'Generating script…';
        let res: engine.ScriptDataResult;
        try {
          res = await App.ScriptData(
            o.connId,
            o.database,
            o.schema,
            o.name,
            engine.ScriptDataOptions.createFrom({
              top: allRows.checked ? 0 : topN,
              where: where.value,
              orderBy: orderBy.value,
              target: target.value,
              rowsPerInsert: Number(perInsert.value),
              includeIdentity: hasIdentity && identity.checked,
              includeCreate: create.checked,
            }),
          );
        } catch (e) {
          status.className = 'test-status err';
          status.textContent = errText(e);
          return false;
        }
        const summary = `${res.rows.toLocaleString()} row${res.rows === 1 ? '' : 's'}${res.truncated ? ' (limited to 200,000)' : ''}`;
        if (v === 'tab') {
          opened = res.script;
          return true;
        }
        if (v === 'copy') {
          await App.CopyText(res.script);
          status.className = 'test-status ok';
          status.textContent = `✓ Copied ${summary} to the clipboard.`;
          return false; // keep the dialog open so the confirmation is visible
        }
        const safe = `${o.name}_data.sql`.replace(/[^\w.-]+/g, '_');
        try {
          const path = await App.ExportText(safe, res.script);
          if (!path) {
            status.textContent = '';
            return false;
          }
          status.className = 'test-status ok';
          status.textContent = `✓ Saved ${summary} to ${path}`;
        } catch (e) {
          status.className = 'test-status err';
          status.textContent = errText(e);
        }
        return false;
      },
    },
  );
  return opened;
}

// ---------- settings ----------

export async function settingsDialog() {
  const s = state.settings;
  const theme = h(
    'select',
    { class: 'input' },
    ...[
      ['system', 'Match system'],
      ['light', 'Light'],
      ['dark', 'Dark'],
    ].map(([v, l]) => h('option', { value: v, selected: s.theme === v }, l)),
  );
  const font = h('input', { class: 'input', type: 'number', min: '9', max: '28', value: String(s.fontSize) });
  const maxRows = h('input', { class: 'input', type: 'number', min: '100', step: '1000', value: String(s.maxRows) });
  const timeout = h('input', { class: 'input', type: 'number', min: '0', value: String(s.queryTimeout) });
  const hist = h('input', { type: 'checkbox', checked: s.historyEnabled });
  const r = await modal(
    'Settings',
    h(
      'div',
      { class: 'form' },
      h('label', { class: 'field' }, h('span', {}, 'Theme'), theme),
      h('label', { class: 'field' }, h('span', {}, 'Editor font size'), font),
      h('label', { class: 'field' }, h('span', {}, 'Max rows per result set'), maxRows),
      h('label', { class: 'field' }, h('span', {}, 'Query timeout in seconds (0 = none)'), timeout),
      h('label', { class: 'check' }, hist, ' Save query history'),
    ),
    [
      { label: 'Cancel', value: 'cancel' },
      { label: 'Save', value: 'ok', primary: true },
    ],
  );
  if (r !== 'ok') return;
  const next = store.Settings.createFrom({
    theme: theme.value,
    fontSize: Math.min(28, Math.max(9, Number(font.value) || 13)),
    maxRows: Math.max(100, Number(maxRows.value) || 100000),
    queryTimeout: Math.max(0, Number(timeout.value) || 0),
    historyEnabled: hist.checked,
  });
  await App.SaveSettings(next);
  state.settings = next;
  applyTheme();
  emit('settings');
}

// ---------- history ----------

export async function historyDialog(onOpen: (query: string, connId: string, db: string) => void) {
  const list = await App.GetHistory();
  const search = h('input', { class: 'input', type: 'search', placeholder: 'Filter history…' });
  const ul = h('div', { class: 'history-list' });
  let chosen: { q: string; c: string; d: string } | null = null;
  const render = () => {
    const q = search.value.toLowerCase();
    ul.replaceChildren(
      ...list
        .filter((e) => !q || e.query.toLowerCase().includes(q))
        .slice(0, 300)
        .map((e) =>
          h(
            'button',
            {
              type: 'button',
              class: 'history-item' + (e.error ? ' failed' : ''),
              ondblclick: () => {
                chosen = { q: e.query, c: e.connectionId, d: e.database };
                (ul.closest('dialog') as HTMLDialogElement)?.querySelector<HTMLButtonElement>('.btn.primary')?.click();
              },
              onclick: (ev: Event) => {
                ul.querySelectorAll('.history-item').forEach((x) => x.classList.remove('on'));
                (ev.currentTarget as HTMLElement).classList.add('on');
                chosen = { q: e.query, c: e.connectionId, d: e.database };
              },
            },
            h('pre', {}, e.query.length > 400 ? e.query.slice(0, 400) + '…' : e.query),
            h(
              'span',
              { class: 'meta' },
              `${new Date(e.time).toLocaleString()} · ${state.conns.get(e.connectionId)?.profile.name ?? 'unknown'}${e.database ? ' / ' + e.database : ''} · ${formatDuration(e.durationMs)}${e.error ? ' · failed' : ''}`,
            ),
          ),
        ),
    );
    if (!ul.children.length) ul.append(h('p', { class: 'muted empty' }, 'No queries yet.'));
  };
  search.addEventListener('input', render);
  render();
  const r = await modal('Query History', h('div', { class: 'form' }, search, ul), [
    { label: 'Clear All', value: 'clear', danger: true },
    { label: 'Close', value: 'cancel' },
    { label: 'Open in New Tab', value: 'open', primary: true },
  ], { wide: true });
  if (r === 'clear' && (await confirmDialog('Clear history', 'Delete all saved query history?', 'Clear', true))) await App.ClearHistory();
  const sel = chosen as { q: string; c: string; d: string } | null;
  if (r === 'open' && sel) onOpen(sel.q, sel.c, sel.d);
}

// ---------- cell viewer ----------

export function cellViewer(value: Cell, col: GridColumn) {
  let text = value ?? 'NULL';
  // Pretty-print JSON / XML for readability.
  const t = text.trim();
  if ((t.startsWith('{') && t.endsWith('}')) || (t.startsWith('[') && t.endsWith(']'))) {
    try {
      text = JSON.stringify(JSON.parse(t), null, 2);
    } catch {
      /* not JSON */
    }
  } else if (t.startsWith('<') && t.endsWith('>') && col.type === 'xml') {
    text = t.replace(/>\s*</g, '>\n<');
  }
  const pre = h('textarea', { class: 'cell-view', readOnly: true, spellcheck: false }, text);
  void modal(`${col.name} (${col.type})`, pre, [
    { label: 'Copy', value: 'copy' },
    { label: 'Close', value: 'close', primary: true },
  ], {
    wide: true,
    onButton: (v) => {
      if (v === 'copy') {
        navigator.clipboard.writeText(text).catch(() => {});
        return false;
      }
      return true;
    },
  });
}

// ---------- device code ----------

export function deviceCodeDialog(message: string, code: string, url: string) {
  void modal(
    'Sign in with device code',
    h(
      'div',
      { class: 'form' },
      h('p', {}, message),
      h('div', { class: 'device-code' }, code),
      h('p', { class: 'muted' }, 'The code has been copied to your clipboard and the sign-in page opened in your browser. This dialog can be closed.'),
    ),
    [
      { label: 'Open Page Again', value: 'open' },
      { label: 'Close', value: 'close', primary: true },
    ],
    {
      onButton: (v) => {
        if (v === 'open') {
          void App.OpenURL(url);
          return false;
        }
        return true;
      },
    },
  );
}

// ---------- context menu ----------

export interface MenuItem {
  label: string;
  action?: () => void;
  separator?: boolean;
  danger?: boolean;
  disabled?: boolean;
  shortcut?: string;
}

let openMenu: HTMLElement | null = null;
export function closeContextMenu() {
  openMenu?.remove();
  openMenu = null;
}

export function contextMenu(x: number, y: number, items: MenuItem[]) {
  closeContextMenu();
  const menu = h(
    'div',
    { class: 'ctx-menu' },
    ...items.map((it) =>
      it.separator
        ? h('div', { class: 'ctx-sep' })
        : h(
            'button',
            {
              class: 'ctx-item' + (it.danger ? ' danger' : ''),
              disabled: !!it.disabled,
              onclick: () => {
                closeContextMenu();
                it.action?.();
              },
            },
            h('span', {}, it.label),
            it.shortcut ? h('kbd', {}, it.shortcut) : null,
          ),
    ),
  );
  document.body.appendChild(menu);
  const r = menu.getBoundingClientRect();
  menu.style.left = Math.min(x, window.innerWidth - r.width - 6) + 'px';
  menu.style.top = Math.min(y, window.innerHeight - r.height - 6) + 'px';
  openMenu = menu;
}

window.addEventListener('mousedown', (e) => {
  if (openMenu && !openMenu.contains(e.target as Node)) closeContextMenu();
});
window.addEventListener('blur', closeContextMenu);
window.addEventListener('keydown', (e) => {
  if (e.key === 'Escape') closeContextMenu();
});
