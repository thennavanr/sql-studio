import './style.css';
import * as App from './wailsjs/go/main/App';
import { EventsOn } from './wailsjs/runtime/runtime';
import { applyTheme, emit, loadConnections, setErrorHandler, setPasswordPrompt, state } from './state';
import { deviceCodeDialog, historyDialog, promptPasswordFor, settingsDialog, showErrorDialog } from './dialogs';
import { Sidebar } from './sidebar';
import { Workspace } from './workspace';
import { h } from './util';
import { icon } from './icons';

setPasswordPrompt(promptPasswordFor);
setErrorHandler(showErrorDialog);

async function boot() {
  try {
    state.settings = await App.GetSettings();
  } catch {
    /* defaults */
  }
  applyTheme();
  matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
    applyTheme();
    emit('settings');
  });

  const sidebar = new Sidebar({
    newQuery: (connId, db, text, run) => ws.newTabFor(connId, db, text, run),
  });
  const ws: Workspace = new Workspace(() => void sidebar.addConnection());

  const resizer = h('div', { class: 'vsplit' });
  const iconBtn = (name: string, title: string, fn: () => void) => h('button', { class: 'icon-btn', title, onclick: fn }, icon(name, 15));
  const titlebar = h(
    'header',
    { class: 'titlebar' },
    h('div', { class: 'traffic-space' }),
    iconBtn('sidebar', 'Toggle sidebar (⌘B)', () => toggleSidebar()),
    ws.toolbar,
    h('div', { class: 'title-spacer' }),
    iconBtn('format', 'Format SQL (⌥⇧F)', () => void ws.format()),
    iconBtn('history', 'Query history (⌘Y)', () => openHistory()),
    iconBtn('settings', 'Settings (⌘,)', () => void settingsDialog()),
  );
  const main = h('div', { class: 'main' }, sidebar.el, resizer, ws.el);
  document.getElementById('app')!.replaceChildren(titlebar, main, ws.statusbar);

  // Sidebar width + toggle.
  let sideW = 270;
  try {
    sideW = Number(localStorage.getItem('sqlstudio.sideW')) || 270;
  } catch {
    /* ignore */
  }
  let sideHidden = false;
  const applySide = () => {
    main.style.gridTemplateColumns = sideHidden ? '0 0 1fr' : `${sideW}px 4px 1fr`;
    sidebar.el.hidden = sideHidden;
  };
  const toggleSidebar = () => {
    sideHidden = !sideHidden;
    applySide();
  };
  applySide();
  resizer.addEventListener('mousedown', (e) => {
    e.preventDefault();
    document.body.classList.add('resizing-h');
    const move = (ev: MouseEvent) => {
      sideW = Math.min(600, Math.max(180, ev.clientX));
      applySide();
    };
    const up = () => {
      document.body.classList.remove('resizing-h');
      window.removeEventListener('mousemove', move);
      window.removeEventListener('mouseup', up);
      try {
        localStorage.setItem('sqlstudio.sideW', String(sideW));
      } catch {
        /* ignore */
      }
    };
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseup', up);
  });

  const openHistory = () =>
    void historyDialog((q, c, d) => {
      ws.newTab({ connId: state.conns.has(c) ? c : undefined, database: d, text: q });
    });

  // Zoom (whole UI).
  let zoom = 1;
  try {
    zoom = Number(localStorage.getItem('sqlstudio.zoom')) || 1;
  } catch {
    /* ignore */
  }
  const applyZoom = () => {
    document.documentElement.style.setProperty('--zoom', String(zoom));
    (document.body.style as any).zoom = String(zoom);
    try {
      localStorage.setItem('sqlstudio.zoom', String(zoom));
    } catch {
      /* ignore */
    }
  };
  applyZoom();

  const commands: Record<string, () => void> = {
    newQuery: () => ws.newTab(),
    newConnection: () => void sidebar.addConnection(),
    open: () => void ws.openFile(),
    save: () => void ws.save(false),
    saveAs: () => void ws.save(true),
    closeTab: () => void ws.closeTab(),
    execute: () => void ws.execute('auto'),
    executeCurrent: () => void ws.execute('current'),
    cancel: () => ws.cancel(),
    format: () => void ws.format(),
    toggleResults: () => ws.toggleResults(),
    toggleSidebar,
    history: openHistory,
    settings: () => void settingsDialog(),
    zoomIn: () => {
      zoom = Math.min(2, +(zoom + 0.1).toFixed(2));
      applyZoom();
    },
    zoomOut: () => {
      zoom = Math.max(0.6, +(zoom - 0.1).toFixed(2));
      applyZoom();
    },
    zoomReset: () => {
      zoom = 1;
      applyZoom();
    },
  };
  EventsOn('menu', (action: string) => commands[action]?.());
  EventsOn('auth:devicecode', (d: any) => deviceCodeDialog(d.message, d.userCode, d.url));

  // Keyboard fallbacks (also used in the browser dev server where there is no native menu).
  window.addEventListener('keydown', (e) => {
    const mod = e.metaKey || e.ctrlKey;
    if (e.key === 'F5') {
      e.preventDefault();
      commands.execute();
    } else if (mod && e.key === '.') {
      e.preventDefault();
      commands.cancel();
    } else if (mod && e.shiftKey && (e.key === ']' || e.key === '}')) {
      e.preventDefault();
      ws.cycleTab(1);
    } else if (mod && e.shiftKey && (e.key === '[' || e.key === '{')) {
      e.preventDefault();
      ws.cycleTab(-1);
    } else if (e.ctrlKey && e.key === 'Tab') {
      e.preventDefault();
      ws.cycleTab(e.shiftKey ? -1 : 1);
    }
  });

  // Prevent the webview from navigating when files are dropped; open .sql files instead.
  window.addEventListener('dragover', (e) => e.preventDefault());
  window.addEventListener('drop', (e) => {
    if (e.dataTransfer?.getData('text/x-tab')) return;
    e.preventDefault();
    const files = [...(e.dataTransfer?.files ?? [])];
    for (const f of files) {
      void f.text().then((text) => ws.newTab({ text, title: f.name }));
    }
  });

  await loadConnections();
  if (!ws.restore()) {
    if (state.conns.size) ws.newTab();
  }
  sidebar.render();
}

void boot();
