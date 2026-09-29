package welcome

import "image"

// copyXRGB copies an RGBA image into an XRGB8888 scanout buffer (little
// endian B, G, R, X per pixel) with the given row pitch, clipping to both.
func copyXRGB(dst []byte, pitch, dw, dh int, img *image.RGBA) {
	w := min(dw, img.Rect.Dx())
	h := min(dh, img.Rect.Dy())
	for y := 0; y < h; y++ {
		if (y+1)*pitch > len(dst) {
			return
		}
		src := img.Pix[y*img.Stride : y*img.Stride+w*4]
		row := dst[y*pitch : y*pitch+w*4]
		for i := 0; i < len(src); i += 4 {
			row[i], row[i+1], row[i+2], row[i+3] = src[i+2], src[i+1], src[i], 0xff
		}
	}
}
