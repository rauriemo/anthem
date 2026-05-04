"""Triage flagged magenta-family pixels by connected-component size.

Produces two outputs:
  1. A triage overlay PNG: small components (likely holes) painted RED,
     large components (likely subject decorations) painted GREEN, so the
     user can validate the size threshold visually.
  2. A kill-applied PNG: small components removed (alpha = 0), large
     components preserved as-is.

Configurable via:
  --size-threshold N    components with <= N pixels are treated as holes
                        (default 70)
  --label-large         when set, also numerically labels the green
                        components on the overlay so you can tell me which
                        ones are wrong
"""
import argparse
import sys
from PIL import Image, ImageDraw, ImageFont
import numpy as np
import cv2


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--input", required=True)
    ap.add_argument("--triage-out", required=True)
    ap.add_argument("--kill-out", required=True)
    ap.add_argument("--size-threshold", type=int, default=70)
    ap.add_argument("--score-threshold", type=float, default=0.25,
                    help="chroma_key keyer threshold for flagging")
    args = ap.parse_args()

    img = np.asarray(Image.open(args.input).convert("RGBA"))
    H, W, _ = img.shape
    R = img[..., 0].astype("int32")
    G = img[..., 1].astype("int32")
    B = img[..., 2].astype("int32")
    A = img[..., 3]

    opaque = A > 0
    score = (np.minimum(R, B) - G) / 255.0
    mag = (score > args.score_threshold) & opaque

    # Connected components
    mag_u8 = mag.astype("uint8") * 255
    n_labels, labels, stats, _centroids = cv2.connectedComponentsWithStats(mag_u8, connectivity=8)
    n_comp = n_labels - 1
    print(f"flagged: {mag.sum()} pixels in {n_comp} components")
    print(f"size threshold for kill: <= {args.size_threshold} px")

    # Classify components
    kill_labels = []
    keep_labels = []
    for label in range(1, n_labels):
        area = int(stats[label, cv2.CC_STAT_AREA])
        if area <= args.size_threshold:
            kill_labels.append(label)
        else:
            keep_labels.append(label)

    kill_mask = np.isin(labels, kill_labels)
    keep_mask = np.isin(labels, keep_labels)
    print(f"kill components: {len(kill_labels)} ({int(kill_mask.sum())} px)")
    print(f"keep components: {len(keep_labels)} ({int(keep_mask.sum())} px)")

    # ---- Triage overlay ----
    overlay = img.copy()
    overlay[kill_mask, 0] = 255
    overlay[kill_mask, 1] = 0
    overlay[kill_mask, 2] = 0
    overlay[kill_mask, 3] = 255
    overlay[keep_mask, 0] = 0
    overlay[keep_mask, 1] = 255
    overlay[keep_mask, 2] = 0
    overlay[keep_mask, 3] = 255

    # Numerically label the green components so user can flag mistakes
    pil_overlay = Image.fromarray(overlay, "RGBA")
    draw = ImageDraw.Draw(pil_overlay)
    try:
        font = ImageFont.truetype("arial.ttf", 18)
    except OSError:
        font = ImageFont.load_default()
    # Sort keep_labels by area descending for stable numbering
    keep_sorted = sorted(keep_labels, key=lambda L: -int(stats[L, cv2.CC_STAT_AREA]))
    for idx, label in enumerate(keep_sorted):
        x = int(stats[label, cv2.CC_STAT_LEFT])
        y = int(stats[label, cv2.CC_STAT_TOP])
        w = int(stats[label, cv2.CC_STAT_WIDTH])
        h = int(stats[label, cv2.CC_STAT_HEIGHT])
        area = int(stats[label, cv2.CC_STAT_AREA])
        # Label slightly above the bbox top-left
        draw.rectangle([x - 1, y - 1, x + w + 1, y + h + 1], outline=(255, 255, 0, 255), width=2)
        label_text = f"#{idx+1}({area})"
        draw.text((x, max(0, y - 22)), label_text, fill=(255, 255, 0, 255), font=font)
    pil_overlay.save(args.triage_out)
    print(f"triage overlay saved: {args.triage_out}")

    # ---- Kill-applied PNG ----
    killed = img.copy()
    killed[kill_mask, 3] = 0  # alpha = 0
    Image.fromarray(killed, "RGBA").save(args.kill_out)
    R2 = killed[..., 0].astype("int32")
    G2 = killed[..., 1].astype("int32")
    B2 = killed[..., 2].astype("int32")
    A2 = killed[..., 3]
    mag_after = ((np.minimum(R2, B2) - G2) / 255.0 > args.score_threshold) & (A2 > 0)
    print(f"kill-applied: {args.kill_out}")
    print(f"  magenta-flagged remaining: {int(mag_after.sum())}")
    print(f"  pixels alpha=0 added: {int(kill_mask.sum())}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
