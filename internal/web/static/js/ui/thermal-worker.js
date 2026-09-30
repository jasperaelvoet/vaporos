// ui/thermal-worker.js: ui/thermal.js's painter: a job in, a bitmap out.

import { paint } from '../heatmap.js';

addEventListener('message', async ({ data }) => {
  try {
    const px = paint(data);
    const bmp = await createImageBitmap(new ImageData(new Uint8ClampedArray(px.buffer), data.W, data.H));
    postMessage({ id: data.id, bmp }, [bmp]);
  } catch (err) {
    postMessage({ id: data.id, error: String(err) });
  }
});
