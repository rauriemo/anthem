"""Probe the actual RGB values of surviving magenta-family pixels in
trinket-salesman.png, and characterize the distance-from-#FF00FF distribution.

Also samples a few "subject magenta-adjacent" candidate pixels (purple potions
on the backpack) to see how far they are from #FF00FF and what tolerance would
be safe.
"""
from PIL import Image
import numpy as np
import sys

src_path = sys.argv[1]
img = np.asarray(Image.open(src_path).convert("RGBA"))
H, W, _ = img.shape
R = img[..., 0].astype("int32")
G = img[..., 1].astype("int32")
B = img[..., 2].astype("int32")
A = img[..., 3]

opaque = A > 0
# chroma_key score for "magenta-family"
score = (np.minimum(R, B) - G) / 255.0
mag = (score > 0.25) & opaque
mag_pixels = np.stack([R[mag], G[mag], B[mag]], axis=-1)

print(f"== magenta-family survivors ({mag.sum()} pixels) ==")
print(f"R range: {mag_pixels[:,0].min()}..{mag_pixels[:,0].max()}, mean={mag_pixels[:,0].mean():.1f}")
print(f"G range: {mag_pixels[:,1].min()}..{mag_pixels[:,1].max()}, mean={mag_pixels[:,1].mean():.1f}")
print(f"B range: {mag_pixels[:,2].min()}..{mag_pixels[:,2].max()}, mean={mag_pixels[:,2].mean():.1f}")

# Distance from pure magenta
target = np.array([255, 0, 255], dtype="int32")
dists = np.sqrt(np.sum((mag_pixels - target) ** 2, axis=-1))
print(f"euclidean distance from #FF00FF: min={dists.min():.1f} max={dists.max():.1f} "
      f"mean={dists.mean():.1f} median={np.median(dists):.1f}")
print(f"  percentiles: 25th={np.percentile(dists, 25):.1f}, "
      f"75th={np.percentile(dists, 75):.1f}, 95th={np.percentile(dists, 95):.1f}, "
      f"99th={np.percentile(dists, 99):.1f}")

# Per-channel max delta
deltas = np.abs(mag_pixels - target)
max_delta_per_pixel = deltas.max(axis=-1)
print(f"per-channel max-delta from (255,0,255): max={max_delta_per_pixel.max()} "
      f"95th={np.percentile(max_delta_per_pixel, 95):.0f}")

# Top 10 most common (R,G,B) tuples among survivors
unique, counts = np.unique(mag_pixels.reshape(-1, 3), axis=0, return_counts=True)
top_idx = np.argsort(counts)[::-1][:10]
print(f"\ntop-10 most common survivor colors:")
for i in top_idx:
    rgb = unique[i]
    cnt = counts[i]
    d = np.linalg.norm(rgb - target)
    print(f"  RGB({rgb[0]:>3},{rgb[1]:>3},{rgb[2]:>3})  n={cnt:>5}  dist={d:.1f}")

# Sample purple-family pixels (potential collateral)
# Purple = G low, R and B both medium-to-high, BUT R != B (vs pure magenta where R==B==255)
purple = (G < 100) & (R > 80) & (B > 80) & opaque & ~mag  # purple-family but NOT magenta-family
print(f"\n== purple-family non-magenta pixels (potential collateral, {purple.sum()}) ==")
if purple.sum() > 0:
    pp = np.stack([R[purple], G[purple], B[purple]], axis=-1)
    pp_dists = np.sqrt(np.sum((pp - target) ** 2, axis=-1))
    print(f"distance from #FF00FF: min={pp_dists.min():.1f} max={pp_dists.max():.1f} "
          f"mean={pp_dists.mean():.1f} median={np.median(pp_dists):.1f}")
    print(f"  percentiles: 5th={np.percentile(pp_dists, 5):.1f}, "
          f"25th={np.percentile(pp_dists, 25):.1f}")
    # Top 5 closest-to-magenta purple pixels (most-at-risk)
    closest = np.argsort(pp_dists)[:5]
    print(f"closest-to-magenta purple pixels (most-at-risk for collateral kill):")
    for i in closest:
        rgb = pp[i]
        print(f"  RGB({rgb[0]:>3},{rgb[1]:>3},{rgb[2]:>3})  dist={pp_dists[i]:.1f}")
