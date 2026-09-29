// Text widths from a TrueType font's own advances (cmap format 4 + hmtx), so
// a share card can fit its headline to the column exactly, whatever the
// content says. Kerning is ignored; callers keep a small margin.

export interface FontMetrics {
  unitsPerEm: number;
  /** Advance of a code point, in font units (0 when the font lacks it). */
  advance(cp: number): number;
}

export function fontMetrics(buf: Uint8Array): FontMetrics {
  const view = new DataView(buf.buffer, buf.byteOffset, buf.byteLength);
  const tables = new Map<string, number>();
  const n = view.getUint16(4);
  for (let i = 0; i < n; i++) {
    const at = 12 + i * 16;
    const tag = String.fromCharCode(view.getUint8(at), view.getUint8(at + 1), view.getUint8(at + 2), view.getUint8(at + 3));
    tables.set(tag, view.getUint32(at + 8));
  }
  const table = (tag: string) => {
    const at = tables.get(tag);
    if (at === undefined) throw new Error(`font has no ${tag} table`);
    return at;
  };
  const unitsPerEm = view.getUint16(table('head') + 18);
  const numberOfHMetrics = view.getUint16(table('hhea') + 34);
  const hmtx = table('hmtx');
  const adv = (glyph: number) => view.getUint16(hmtx + Math.min(glyph, numberOfHMetrics - 1) * 4);

  // cmap: the first Unicode BMP subtable in format 4.
  const cmap = table('cmap');
  let sub = -1;
  for (let i = 0, count = view.getUint16(cmap + 2); i < count; i++) {
    const platform = view.getUint16(cmap + 4 + i * 8);
    const encoding = view.getUint16(cmap + 6 + i * 8);
    const offset = cmap + view.getUint32(cmap + 8 + i * 8);
    if ((platform === 3 && encoding === 1) || platform === 0) {
      if (view.getUint16(offset) === 4) {
        sub = offset;
        break;
      }
    }
  }
  if (sub < 0) throw new Error('font has no format-4 Unicode cmap');
  const segX2 = view.getUint16(sub + 6);
  const ends = sub + 14;
  const starts = ends + segX2 + 2;
  const deltas = starts + segX2;
  const ranges = deltas + segX2;
  const glyphOf = (cp: number) => {
    if (cp > 0xffff) return 0;
    for (let s = 0; s < segX2; s += 2) {
      if (view.getUint16(ends + s) < cp) continue;
      const start = view.getUint16(starts + s);
      if (start > cp) return 0;
      const delta = view.getInt16(deltas + s);
      const ro = view.getUint16(ranges + s);
      if (!ro) return (cp + delta) & 0xffff;
      const g = view.getUint16(ranges + s + ro + (cp - start) * 2);
      return g ? (g + delta) & 0xffff : 0;
    }
    return 0;
  };
  const cache = new Map<number, number>();
  return {
    unitsPerEm,
    advance(cp) {
      if (!cache.has(cp)) {
        const g = glyphOf(cp);
        cache.set(cp, g ? adv(g) : 0);
      }
      return cache.get(cp)!;
    },
  };
}

/** Width of `text` at `size` px with `tracking` em of letter spacing. */
export function textWidth(m: FontMetrics, text: string, size: number, tracking = 0): number {
  const cps = [...text].map((c) => c.codePointAt(0)!);
  const units = cps.reduce((w, cp) => w + m.advance(cp), 0);
  return (units / m.unitsPerEm) * size + tracking * size * Math.max(0, cps.length - 1);
}

/** The largest size ≤ max (whole px) at which every line fits `width`. */
export function fitSize(m: FontMetrics, lines: string[], width: number, max: number, tracking = 0): number {
  const widest = Math.max(...lines.map((l) => textWidth(m, l, 100, tracking)));
  return Math.max(12, Math.min(max, Math.floor((width / widest) * 100)));
}
