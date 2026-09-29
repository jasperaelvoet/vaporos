// A decorative QR-like pattern (NOT a real, scannable code) for illustrations
// of the welcome screen. Deterministic, so server and client render the same
// cells. Ported from the Astro site's DeviceMock.astro.

/** size x size booleans, row by row; true = dark cell. Three finder squares, pseudo-random data. */
export function qrPattern(size = 21, seed = 7): boolean[] {
  const cells: boolean[] = [];
  const far = size - 7;
  let s = seed;
  for (let i = 0; i < size * size; i++) {
    s = (s * 1103515245 + 12345) & 0x7fffffff;
    const x = i % size;
    const y = Math.floor(i / size);
    const finder = (x < 7 && y < 7) || (x >= far && y < 7) || (x < 7 && y >= far);
    if (finder) {
      const fx = x >= far ? x - far : x;
      const fy = y >= far ? y - far : y;
      cells.push(fx === 0 || fx === 6 || fy === 0 || fy === 6 || (fx > 1 && fx < 5 && fy > 1 && fy < 5));
    } else {
      cells.push(((s >> 16) & 3) === 0 || ((s >> 12) & 1) === 1);
    }
  }
  return cells;
}
