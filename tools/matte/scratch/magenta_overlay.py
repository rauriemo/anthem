"""Visualize which pixels my magenta-audit script is flagging.

Paints flagged pixels bright cyan on top of the original image so the user
can confirm whether they're (a) actual interior plate pockets or (b) subject
features like purple potions being false-positives.

Also produces a *connected components* analysis: for each blob of flagged
pixels, prints size + dominant color + distance from #FF00FF + bounding box.
"""
from PIL import Image
import numpy as np
import cv2
import sys

src_path, out_path = sys.argv[1], sys.argv[2]
img = np.asarray(Image.open(src_path).convert("RGBA"))
H, W, _ = img.shape
R = img[..., 0].astype("int32")
G = img[..., 1].astype("int32")
B = img[..., 2].astype("int32")
A = img[..., 3]

opaque = A > 0
score = (np.minimum(R, B) - G) / 255.0
mag = (score > 0.25) & opaque

# Connected components on the magenta mask (via cv2)
mag_u8 = mag.astype("uint8") * 255
n_labels, labels, stats, centroids = cv2.connectedComponentsWithStats(mag_u8, connectivity=8)
n_comp = n_labels - 1  # subtract background label 0
print(f"flagged pixels: {mag.sum()}, connected components: {n_comp}")

# Per-component stats (sorted by area descending)
print(f"\n{'#':<4} {'size':>6} {'centroid(y,x)':>14} {'mean RGB':>20} {'dist#FF00FF':>11}  {'bbox(x,y,w,h)':>15}")
print("-" * 88)
target = np.array([255, 0, 255], dtype="int32")
# stats columns: x, y, w, h, area; row 0 is background, skip it
areas = stats[1:, cv2.CC_STAT_AREA]
order = np.argsort(areas)[::-1]
for rank, ci in enumerate(order[:30]):
    label = ci + 1
    component_mask = (labels == label)
    n = int(stats[label, cv2.CC_STAT_AREA])
    cx, cy = int(centroids[label][0]), int(centroids[label][1])
    rr = int(R[component_mask].mean())
    gg = int(G[component_mask].mean())
    bb = int(B[component_mask].mean())
    dist = np.sqrt((rr - 255) ** 2 + (gg - 0) ** 2 + (bb - 255) ** 2)
    bx = int(stats[label, cv2.CC_STAT_LEFT])
    by = int(stats[label, cv2.CC_STAT_TOP])
    bw = int(stats[label, cv2.CC_STAT_WIDTH])
    bh = int(stats[label, cv2.CC_STAT_HEIGHT])
    print(f"{rank+1:<4} {n:>6} {f'({cy},{cx})':>14} {f'({rr},{gg},{bb})':>20} {dist:>11.1f}  {f'({bx},{by},{bw},{bh})':>15}")

# Composite: RGB from source, opaque where original was opaque, flagged pixels painted bright cyan with full opacity
overlay = img.copy()
overlay[mag, 0] = 0    # R
overlay[mag, 1] = 255  # G
overlay[mag, 2] = 255  # B
overlay[mag, 3] = 255  # A
Image.fromarray(overlay, "RGBA").save(out_path)
print(f"\noverlay saved: {out_path}")
