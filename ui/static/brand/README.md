# sap13ns brand assets

The mark is an isometric "S" made of two ribbons.

## Colours

| Name      | Hex       | Use                                              |
|-----------|-----------|--------------------------------------------------|
| Violet    | `#3F068B` | first ribbon (top panel and side faces)          |
| Turquoise | `#0AE3DA` | second ribbon (top faces and bottom panel)       |
| Ink       | `#0E0B1A` | background tile for the opaque icons only        |

Violet and turquoise were sampled from the dominant opaque pixels of the source
PNG (k-means mean, mode and median all agree).

## Files

| File                          | What it is                                                                                   |
|-------------------------------|----------------------------------------------------------------------------------------------|
| `logo.svg`                    | Full mark, 2 flat fills, square `0 0 256 256` viewBox with 6% padding.                       |
| `logo-mono.svg`               | Same paths in `currentColor`; the turquoise ribbon is drawn at `opacity=".55"`.              |
| `../favicon.svg`              | Mark tuned for 16–32 px: gaps opened up, violet side faces thickened, tighter crop, 64 viewBox.|
| `favicon-32.png`              | 32×32, transparent, from `favicon.svg`.                                                      |
| `icon-192.png`, `icon-512.png`| Transparent, from `logo.svg`.                                                                |
| `apple-touch-icon.png`        | 180×180 on an ink tile (opaque, square; iOS rounds the corners). Mark is 66% of the height.  |
| `icon-maskable-512.png`       | 512×512 on an ink tile. The mark reaches at most 34% of the size from the centre (safe zone: 40%). |

`logo-mono.svg` only picks up the surrounding text colour when it is inlined
(or used through `<svg><use href>`). Loaded through `<img>`, it renders black.

In the SVGs the turquoise path is extended a few pixels under the violet one,
so the two fills overlap rather than meeting edge to edge, which leaves no
anti-aliasing seam. For this reason `logo.svg` draws turquoise first. In
`logo-mono.svg` the violet path is drawn solid and the turquoise one over it at
.55. A solid fill plus a .55 fill of the same colour is still solid, so the
hidden overlap stays invisible.

## How they were generated

Everything came from the source PNG (1254×1254 RGBA) by script. Nothing was
drawn by hand.

1. **Split (Go, `image/png` + `golang.org/x/image/draw`).** Each pixel was
   projected onto the violet–turquoise colour segment to get soft coverage for
   each ink. Colours far from that segment, which are the background-removal
   specks, were faded out. Both coverage maps were upsampled ×2 (Catmull-Rom)
   and thresholded at 0.5. The masks were cleaned with a small close and with
   island/hole removal (< 400 px²). Then the union was split into violet and
   turquoise, and the turquoise mask was extended 4 px under the violet one.
2. **Trace.** Each mask was traced with potrace (the npm JS port). The outlines
   were then re-fitted with Schneider's least-squares Bézier fitting (`fit-curve`,
   max error 0.4 source px), splitting only at true corners (turn > 46° over ±4 px).
   Coordinates were rounded to 0.1 viewBox unit and written as relative path data.
3. **Favicon.** It used the same pipeline with extra steps: the violet was grown
   26 px into the turquoise, the outline was inset 12 px (this widens every gap by
   24 px), and the fit tolerance was looser. The crop is tighter, with 2% padding.
4. **PNGs.** Headless Chrome rendered them from the SVGs at their exact pixel
   sizes (sRGB). The tiles are solid ink.
5. **Check.** `logo.svg` was rendered over the source frame and compared pixel
   by pixel. Intersection-over-union is 0.996 for violet and 0.995 for
   turquoise. The remaining differences are 1 px anti-aliasing fringes and the
   removed specks.
