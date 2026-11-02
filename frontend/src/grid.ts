// A small virtualized data grid: renders only visible rows so result sets with
// hundreds of thousands of rows stay smooth. Supports streaming appends,
// rectangular cell selection, copy as TSV, column resize and client-side sort.

export interface GridColumn {
  name: string;
  type: string;
  numeric: boolean;
}

export type Cell = string | null;

const ROW_H = 24;
const GUTTER_MIN = 44;
const OVERSCAN = 12;

interface Sel {
  r0: number;
  c0: number;
  r1: number;
  c1: number;
}

export class DataGrid {
  readonly el: HTMLElement;
  private scroller: HTMLElement;
  private header: HTMLElement;
  private body: HTMLElement;
  private sizer: HTMLElement;
  private cols: GridColumn[] = [];
  private widths: number[] = [];
  private rows: Cell[][] = [];
  private view: number[] | null = null; // sorted index, or null = natural order
  private sort: { col: number; dir: 1 | -1 } | null = null;
  private sel: Sel | null = null;
  private anchor: { r: number; c: number } | null = null;
  private gutter = GUTTER_MIN;
  private raf = 0;
  private measured = false;
  onCellOpen?: (value: Cell, col: GridColumn) => void;
  onContextMenu?: (e: MouseEvent) => void;
  onSelectionChange?: (summary: string) => void;

  constructor() {
    this.el = document.createElement('div');
    this.el.className = 'grid';
    this.el.tabIndex = 0;
    this.scroller = document.createElement('div');
    this.scroller.className = 'grid-scroll';
    this.header = document.createElement('div');
    this.header.className = 'grid-header';
    this.sizer = document.createElement('div');
    this.sizer.className = 'grid-sizer';
    this.body = document.createElement('div');
    this.body.className = 'grid-body';
    this.sizer.appendChild(this.body);
    this.scroller.append(this.header, this.sizer);
    this.el.appendChild(this.scroller);

    this.scroller.addEventListener('scroll', () => this.schedule());
    new ResizeObserver(() => this.schedule()).observe(this.scroller);
    this.body.addEventListener('mousedown', (e) => this.onMouseDown(e));
    this.body.addEventListener('dblclick', (e) => {
      const hit = this.hit(e);
      if (hit && hit.c >= 0 && this.onCellOpen) this.onCellOpen(this.value(hit.r, hit.c), this.cols[hit.c]);
    });
    this.el.addEventListener('keydown', (e) => this.onKey(e));
    this.el.addEventListener('copy', (e) => {
      if (!this.sel) return;
      e.preventDefault();
      e.clipboardData?.setData('text/plain', this.selectionText(false));
    });
    this.body.addEventListener('contextmenu', (e) => {
      const hit = this.hit(e);
      if (hit && hit.c >= 0 && !this.inSel(hit.r, hit.c)) this.select({ r0: hit.r, c0: hit.c, r1: hit.r, c1: hit.c });
      e.preventDefault();
      this.onContextMenu?.(e);
    });
  }

  setColumns(cols: GridColumn[]) {
    this.cols = cols;
    this.rows = [];
    this.view = null;
    this.sort = null;
    this.sel = null;
    this.measured = false;
    this.widths = cols.map((c) => Math.min(320, Math.max(64, c.name.length * 8 + 28)));
    this.renderHeader();
    this.schedule();
  }

  append(rows: Cell[][]) {
    const before = this.rows.length;
    for (const r of rows) this.rows.push(r);
    if (!this.measured && (this.rows.length >= 100 || before === 0)) this.measure();
    if (this.sort) this.applySort();
    this.schedule();
  }

  get rowCount() {
    return this.rows.length;
  }
  get columns() {
    return this.cols;
  }

  /** Rows in display (sorted) order. */
  displayRows(): Cell[][] {
    return this.view ? this.view.map((i) => this.rows[i]) : this.rows;
  }

  private measure() {
    const sample = this.rows.slice(0, 100);
    this.widths = this.cols.map((c, i) => {
      let max = c.name.length + 2;
      for (const r of sample) {
        const v = r[i];
        const len = v == null ? 4 : Math.min(v.length, 60);
        if (len > max) max = len;
      }
      return Math.min(360, Math.max(56, Math.round(max * 7.4) + 20));
    });
    this.measured = this.rows.length >= 100;
    this.renderHeader();
  }

  private template() {
    return `${this.gutter}px ${this.widths.map((w) => w + 'px').join(' ')}`;
  }

  private renderHeader() {
    this.gutter = Math.max(GUTTER_MIN, String(this.rows.length).length * 8 + 18);
    this.el.style.setProperty('--grid-cols', this.template());
    const total = this.gutter + this.widths.reduce((a, b) => a + b, 0);
    this.el.style.setProperty('--grid-width', total + 'px');
    this.header.innerHTML = '';
    const corner = document.createElement('div');
    corner.className = 'gh corner';
    corner.title = 'Select all';
    corner.onclick = () => this.selectAll();
    this.header.appendChild(corner);
    this.cols.forEach((c, i) => {
      const h = document.createElement('div');
      h.className = 'gh' + (c.numeric ? ' num' : '');
      const label = document.createElement('span');
      label.className = 'gh-label';
      label.textContent = c.name || '(No column name)';
      h.title = `${c.name} (${c.type})\nClick to sort`;
      h.appendChild(label);
      if (this.sort?.col === i) {
        const arrow = document.createElement('span');
        arrow.className = 'gh-sort';
        arrow.textContent = this.sort.dir === 1 ? '▲' : '▼';
        h.appendChild(arrow);
      }
      h.onclick = (e) => {
        if ((e.target as HTMLElement).classList.contains('gh-resize')) return;
        this.toggleSort(i);
      };
      const rz = document.createElement('div');
      rz.className = 'gh-resize';
      rz.onmousedown = (e) => this.startResize(e, i);
      rz.ondblclick = (e) => {
        e.stopPropagation();
        this.autoFit(i);
      };
      h.appendChild(rz);
      this.header.appendChild(h);
    });
  }

  private startResize(e: MouseEvent, i: number) {
    e.preventDefault();
    e.stopPropagation();
    const startX = e.clientX;
    const startW = this.widths[i];
    const move = (ev: MouseEvent) => {
      this.widths[i] = Math.max(36, startW + ev.clientX - startX);
      this.el.style.setProperty('--grid-cols', this.template());
      this.el.style.setProperty('--grid-width', this.gutter + this.widths.reduce((a, b) => a + b, 0) + 'px');
    };
    const up = () => {
      window.removeEventListener('mousemove', move);
      window.removeEventListener('mouseup', up);
      this.measured = true;
    };
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseup', up);
  }

  private autoFit(i: number) {
    let max = this.cols[i].name.length + 2;
    const n = Math.min(this.rows.length, 2000);
    for (let r = 0; r < n; r++) {
      const v = this.rows[r][i];
      max = Math.max(max, v == null ? 4 : Math.min(v.length, 200));
    }
    this.widths[i] = Math.round(max * 7.4) + 20;
    this.measured = true;
    this.renderHeader();
    this.schedule();
  }

  private toggleSort(i: number) {
    if (!this.sort || this.sort.col !== i) this.sort = { col: i, dir: 1 };
    else if (this.sort.dir === 1) this.sort.dir = -1;
    else this.sort = null;
    this.applySort();
    this.renderHeader();
    this.schedule();
  }

  private applySort() {
    if (!this.sort) {
      this.view = null;
      return;
    }
    const { col, dir } = this.sort;
    const numeric = this.cols[col].numeric;
    const idx = this.rows.map((_, i) => i);
    const coll = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });
    idx.sort((a, b) => {
      const x = this.rows[a][col];
      const y = this.rows[b][col];
      if (x === y) return 0;
      if (x == null) return -dir;
      if (y == null) return dir;
      if (numeric) {
        const d = Number(x) - Number(y);
        if (!Number.isNaN(d) && d !== 0) return d * dir;
      }
      return coll.compare(x, y) * dir;
    });
    this.view = idx;
  }

  private value(r: number, c: number): Cell {
    const row = this.view ? this.rows[this.view[r]] : this.rows[r];
    return row ? row[c] : null;
  }

  private schedule() {
    if (this.raf) return;
    this.raf = requestAnimationFrame(() => {
      this.raf = 0;
      this.render();
    });
  }

  private render() {
    const g = Math.max(GUTTER_MIN, String(this.rows.length).length * 8 + 18);
    if (g !== this.gutter) this.renderHeader();
    this.sizer.style.height = this.rows.length * ROW_H + 'px';
    const top = this.scroller.scrollTop;
    const h = this.scroller.clientHeight;
    const first = Math.max(0, Math.floor(top / ROW_H) - OVERSCAN);
    const last = Math.min(this.rows.length, Math.ceil((top + h) / ROW_H) + OVERSCAN);
    this.body.style.transform = `translateY(${first * ROW_H}px)`;
    const frag = document.createDocumentFragment();
    for (let r = first; r < last; r++) {
      const rowEl = document.createElement('div');
      rowEl.className = 'gr' + (r % 2 ? ' odd' : '');
      rowEl.dataset.r = String(r);
      const num = document.createElement('div');
      num.className = 'gc rn';
      num.textContent = String(r + 1);
      rowEl.appendChild(num);
      for (let c = 0; c < this.cols.length; c++) {
        const v = this.value(r, c);
        const cell = document.createElement('div');
        let cls = 'gc';
        if (v == null) {
          cls += ' null';
          cell.textContent = 'NULL';
        } else {
          cell.textContent = v.length > 500 ? v.slice(0, 500) + '…' : v;
          if (this.cols[c].numeric) cls += ' num';
        }
        if (this.inSel(r, c)) cls += ' sel';
        cell.className = cls;
        rowEl.appendChild(cell);
      }
      frag.appendChild(rowEl);
    }
    this.body.replaceChildren(frag);
  }

  private hit(e: MouseEvent): { r: number; c: number } | null {
    const cell = (e.target as HTMLElement).closest('.gc') as HTMLElement | null;
    const row = cell?.parentElement as HTMLElement | null;
    if (!cell || !row?.dataset.r) return null;
    const r = Number(row.dataset.r);
    const c = Array.prototype.indexOf.call(row.children, cell) - 1; // -1 => row number
    return { r, c };
  }

  private inSel(r: number, c: number) {
    const s = this.sel;
    if (!s) return false;
    return r >= Math.min(s.r0, s.r1) && r <= Math.max(s.r0, s.r1) && c >= Math.min(s.c0, s.c1) && c <= Math.max(s.c0, s.c1);
  }

  private select(s: Sel | null) {
    this.sel = s;
    this.schedule();
    if (this.onSelectionChange) this.onSelectionChange(this.selectionSummary());
  }

  selectAll() {
    if (!this.rows.length) return;
    this.select({ r0: 0, c0: 0, r1: this.rows.length - 1, c1: this.cols.length - 1 });
    this.el.focus();
  }

  private onMouseDown(e: MouseEvent) {
    if (e.button !== 0) return;
    const hit = this.hit(e);
    if (!hit) return;
    this.el.focus();
    if (hit.c < 0) {
      // Row-number click selects whole row(s).
      const r0 = e.shiftKey && this.anchor ? this.anchor.r : hit.r;
      if (!e.shiftKey) this.anchor = { r: hit.r, c: 0 };
      this.select({ r0, c0: 0, r1: hit.r, c1: this.cols.length - 1 });
      return;
    }
    if (e.shiftKey && this.anchor) {
      this.select({ r0: this.anchor.r, c0: this.anchor.c, r1: hit.r, c1: hit.c });
    } else {
      this.anchor = hit;
      this.select({ r0: hit.r, c0: hit.c, r1: hit.r, c1: hit.c });
    }
    const move = (ev: MouseEvent) => {
      const h = this.hit(ev);
      if (h && h.c >= 0 && this.anchor) {
        const s = { r0: this.anchor.r, c0: this.anchor.c, r1: h.r, c1: h.c };
        if (!this.sel || s.r1 !== this.sel.r1 || s.c1 !== this.sel.c1) this.select(s);
      }
    };
    const up = () => {
      window.removeEventListener('mousemove', move);
      window.removeEventListener('mouseup', up);
    };
    window.addEventListener('mousemove', move);
    window.addEventListener('mouseup', up);
  }

  private onKey(e: KeyboardEvent) {
    const mod = e.metaKey || e.ctrlKey;
    if (mod && e.key === 'a') {
      e.preventDefault();
      this.selectAll();
      return;
    }
    if (mod && e.key === 'c') {
      if (!this.sel) return;
      e.preventDefault();
      navigator.clipboard.writeText(this.selectionText(e.shiftKey)).catch(() => {});
      return;
    }
    const s = this.sel;
    if (!s) return;
    const moves: Record<string, [number, number]> = {
      ArrowUp: [-1, 0],
      ArrowDown: [1, 0],
      ArrowLeft: [0, -1],
      ArrowRight: [0, 1],
      PageUp: [-Math.floor(this.scroller.clientHeight / ROW_H), 0],
      PageDown: [Math.floor(this.scroller.clientHeight / ROW_H), 0],
    };
    const mv = moves[e.key];
    if (mv) {
      e.preventDefault();
      const r = Math.min(this.rows.length - 1, Math.max(0, s.r1 + mv[0]));
      const c = Math.min(this.cols.length - 1, Math.max(0, s.c1 + mv[1]));
      if (e.shiftKey) this.select({ ...s, r1: r, c1: c });
      else {
        this.anchor = { r, c };
        this.select({ r0: r, c0: c, r1: r, c1: c });
      }
      this.ensureVisible(r);
    } else if (e.key === 'Enter' && this.onCellOpen) {
      this.onCellOpen(this.value(s.r1, s.c1), this.cols[s.c1]);
    }
  }

  private ensureVisible(r: number) {
    const top = r * ROW_H;
    const st = this.scroller.scrollTop;
    const h = this.scroller.clientHeight - ROW_H; // header
    if (top < st) this.scroller.scrollTop = top;
    else if (top + ROW_H > st + h) this.scroller.scrollTop = top + ROW_H - h;
  }

  private bounds() {
    const s = this.sel!;
    return {
      r0: Math.min(s.r0, s.r1),
      r1: Math.max(s.r0, s.r1),
      c0: Math.min(s.c0, s.c1),
      c1: Math.max(s.c0, s.c1),
    };
  }

  /** Selected cells as TSV (Excel-friendly). */
  selectionText(withHeaders: boolean): string {
    if (!this.sel) return '';
    const { r0, r1, c0, c1 } = this.bounds();
    const lines: string[] = [];
    if (withHeaders) lines.push(this.cols.slice(c0, c1 + 1).map((c) => c.name).join('\t'));
    for (let r = r0; r <= r1; r++) {
      const parts: string[] = [];
      for (let c = c0; c <= c1; c++) {
        const v = this.value(r, c);
        parts.push(v == null ? 'NULL' : v.replace(/[\t\r\n]+/g, ' '));
      }
      lines.push(parts.join('\t'));
    }
    return lines.join('\n');
  }

  /** Selected rows/cols as plain data (for "copy as" formats). */
  selectionData(): { cols: GridColumn[]; rows: Cell[][] } {
    if (!this.sel) return { cols: this.cols, rows: [] };
    const { r0, r1, c0, c1 } = this.bounds();
    const rows: Cell[][] = [];
    for (let r = r0; r <= r1; r++) {
      const row: Cell[] = [];
      for (let c = c0; c <= c1; c++) row.push(this.value(r, c));
      rows.push(row);
    }
    return { cols: this.cols.slice(c0, c1 + 1), rows };
  }

  hasSelection() {
    return this.sel != null;
  }

  private selectionSummary(): string {
    if (!this.sel) return '';
    const { r0, r1, c0, c1 } = this.bounds();
    const n = (r1 - r0 + 1) * (c1 - c0 + 1);
    if (n <= 1) return '';
    let sum = 0;
    let count = 0;
    let numeric = true;
    if (n <= 200000) {
      for (let r = r0; r <= r1 && numeric; r++)
        for (let c = c0; c <= c1; c++) {
          const v = this.value(r, c);
          if (v == null) continue;
          const x = Number(v);
          if (Number.isNaN(x) || v.trim() === '') {
            numeric = false;
            break;
          }
          sum += x;
          count++;
        }
    } else numeric = false;
    let text = `${n.toLocaleString()} cells`;
    if (numeric && count > 0) text += ` · Sum ${+sum.toFixed(6)} · Avg ${+(sum / count).toFixed(6)}`;
    return text;
  }
}
