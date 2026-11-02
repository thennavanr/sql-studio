import * as App from './wailsjs/go/main/App';
import type { engine, main, store } from './wailsjs/go/models';
import { errText } from './util';

export type Connection = store.Connection;
export type Settings = store.Settings;

export interface ConnState {
  profile: Connection;
  status: 'disconnected' | 'connecting' | 'connected' | 'error';
  info?: engine.ServerInfo;
  databases?: string[];
  error?: string;
  pending?: Promise<boolean>;
}

type Topic = 'connections' | 'settings' | 'activeTab';
const listeners: Record<Topic, Set<() => void>> = {
  connections: new Set(),
  settings: new Set(),
  activeTab: new Set(),
};

export function on(topic: Topic, fn: () => void) {
  listeners[topic].add(fn);
}
export function emit(topic: Topic) {
  listeners[topic].forEach((fn) => fn());
}

export const state = {
  conns: new Map<string, ConnState>(),
  settings: { theme: 'system', fontSize: 13, maxRows: 100000, queryTimeout: 0, historyEnabled: true } as Settings,
};

export async function loadConnections() {
  const list = await App.ListConnections();
  const next = new Map<string, ConnState>();
  for (const p of list) {
    const prev = state.conns.get(p.id);
    next.set(p.id, prev ? { ...prev, profile: p } : { profile: p, status: 'disconnected' });
  }
  state.conns = next;
  emit('connections');
}

export function connLabel(id: string | undefined): string {
  if (!id) return '';
  return state.conns.get(id)?.profile.name ?? '';
}

// Injected by dialogs.ts to avoid a circular import.
export let promptPassword: (c: Connection) => Promise<string | null> = async () => null;
export function setPasswordPrompt(fn: typeof promptPassword) {
  promptPassword = fn;
}
export let showError: (title: string, msg: string) => void = (t, m) => alert(t + '\n\n' + m);
export function setErrorHandler(fn: typeof showError) {
  showError = fn;
}

/** Connects (prompting for a password if needed). Resolves true on success. */
export function ensureConnected(id: string): Promise<boolean> {
  const cs = state.conns.get(id);
  if (!cs) return Promise.resolve(false);
  if (cs.status === 'connected') return Promise.resolve(true);
  if (cs.pending) return cs.pending;
  cs.status = 'connecting';
  cs.error = undefined;
  emit('connections');
  cs.pending = (async () => {
    let password = '';
    for (;;) {
      try {
        const res: main.ConnectResult = await App.Connect(id, password);
        cs.info = res.info;
        cs.databases = res.databases;
        cs.status = 'connected';
        return true;
      } catch (e) {
        const msg = errText(e);
        if (msg === 'PASSWORD_REQUIRED' || (password !== '' && /Login failed/i.test(msg))) {
          const pw = await promptPassword(cs.profile);
          if (pw == null) {
            cs.status = 'disconnected';
            return false;
          }
          password = pw;
          continue;
        }
        cs.status = 'error';
        cs.error = msg;
        showError(`Could not connect to ${cs.profile.name}`, msg);
        return false;
      }
    }
  })().finally(() => {
    cs.pending = undefined;
    emit('connections');
  });
  return cs.pending;
}

export async function disconnect(id: string) {
  await App.Disconnect(id);
  const cs = state.conns.get(id);
  if (cs) {
    cs.status = 'disconnected';
    cs.databases = undefined;
    cs.info = undefined;
  }
  emit('connections');
}

export function applyTheme() {
  const pref = state.settings.theme || 'system';
  const dark = pref === 'dark' || (pref === 'system' && matchMedia('(prefers-color-scheme: dark)').matches);
  document.documentElement.dataset.theme = pref;
  document.documentElement.dataset.resolvedTheme = dark ? 'dark' : 'light';
}
