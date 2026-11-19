// Themed tooltips. Native `title` tooltips in WKWebView are drawn by macOS and
// ignore the app's light/dark theme (and are hard to read in dark mode), so
// `title` attributes are taken over on hover and shown in a styled element.

const DELAY = 450;

export function installTooltips() {
  const tip = document.createElement('div');
  tip.className = 'tooltip';
  tip.setAttribute('role', 'tooltip');
  tip.hidden = true;
  document.body.append(tip);

  let target: Element | null = null;
  let timer = 0;
  let x = 0;
  let y = 0;

  const hide = () => {
    clearTimeout(timer);
    target = null;
    tip.hidden = true;
  };

  const show = () => {
    if (!target || !target.isConnected) return hide();
    const text = (target as HTMLElement).dataset?.tip ?? target.getAttribute('data-tip');
    if (!text) return;
    // Inside an open <dialog> (top layer) the tooltip must live in the dialog.
    const host = target.closest('dialog[open]') ?? document.body;
    if (tip.parentElement !== host) host.append(tip);
    tip.textContent = text;
    tip.hidden = false;
    const r = tip.getBoundingClientRect();
    let left = x + 12;
    let top = y + 18;
    if (left + r.width > window.innerWidth - 8) left = Math.max(8, window.innerWidth - r.width - 8);
    if (top + r.height > window.innerHeight - 8) top = Math.max(8, y - r.height - 10);
    tip.style.left = `${left}px`;
    tip.style.top = `${top}px`;
  };

  document.addEventListener(
    'mouseover',
    (e) => {
      const el = (e.target as Element | null)?.closest?.('[title], [data-tip]') ?? null;
      if (el === target) return;
      hide();
      if (!el) return;
      // Take over the native tooltip.
      const title = el.getAttribute('title');
      if (title !== null) {
        el.removeAttribute('title');
        if (title) el.setAttribute('data-tip', title);
      }
      if (!el.getAttribute('data-tip')) return;
      target = el;
      timer = window.setTimeout(show, DELAY);
    },
    true,
  );
  document.addEventListener(
    'mousemove',
    (e) => {
      x = e.clientX;
      y = e.clientY;
    },
    true,
  );
  document.addEventListener(
    'mouseout',
    (e) => {
      if (target && !target.contains(e.relatedTarget as Node | null)) hide();
    },
    true,
  );
  for (const ev of ['mousedown', 'wheel', 'keydown', 'dragstart', 'blur']) {
    window.addEventListener(ev, hide, true);
  }
}
