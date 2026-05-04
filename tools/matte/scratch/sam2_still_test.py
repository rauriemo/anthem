"""SAM2ImagePredictor (image API) on a single still image.

Strategy: BiRefNet seed mask + tight bounding box derived from the BiRefNet
mask. Two prompt variants tested:

  variant=mask_box  -> mask + box together (dense + sparse prompts)
  variant=box_only  -> box only (no mask seed; pure SAM 2 from-scratch on a box)

The mask_box variant most closely mirrors what _sam2_propagate does at video
keyframes (which is BiRefNet seed binarized + propagated). The box_only
variant tests whether SAM 2 alone (without BiRefNet's interior-pocket
contamination passed through as a seed) handles the failure mode better.

Usage:
    python sam2_still_test.py <input_with_magenta_plate.png> <output.png> <variant>
"""
import os
import sys
import time
from pathlib import Path

import numpy as np
import torch
from PIL import Image

# Re-use BiRefNet from the parent matte sidecar.
SCRATCH_DIR = Path(__file__).resolve().parent
MATTE_DIR = SCRATCH_DIR.parent
sys.path.insert(0, str(MATTE_DIR))
import matte  # noqa: E402


def _bbox_from_mask(mask: np.ndarray) -> tuple[int, int, int, int]:
    """Return XYXY bbox of nonzero pixels in mask, padded by 4 px."""
    rows = np.any(mask, axis=1)
    cols = np.any(mask, axis=0)
    if not rows.any():
        H, W = mask.shape
        return (0, 0, W, H)
    rmin, rmax = np.where(rows)[0][[0, -1]]
    cmin, cmax = np.where(cols)[0][[0, -1]]
    pad = 4
    H, W = mask.shape
    return (
        max(0, int(cmin) - pad),
        max(0, int(rmin) - pad),
        min(W, int(cmax) + 1 + pad),
        min(H, int(rmax) + 1 + pad),
    )


def main() -> int:
    src_path, out_path, variant = sys.argv[1], sys.argv[2], sys.argv[3]
    assert variant in ("mask_box", "box_only"), f"unknown variant: {variant}"

    device = matte._device()
    print(f"device: {device}, variant: {variant}")

    # 1) BiRefNet seed mask
    t = time.time()
    _, birefnet_mask = matte._birefnet_infer(Path(src_path), device)
    print(f"birefnet seed: shape={birefnet_mask.shape} elapsed={time.time()-t:.1f}s")
    bin_seed = (birefnet_mask > 127).astype("uint8")
    box = _bbox_from_mask(bin_seed)
    print(f"bbox from birefnet mask: {box}")

    # 2) Build SAM 2 image predictor
    from sam2.build_sam import build_sam2
    from sam2.sam2_image_predictor import SAM2ImagePredictor

    sam2_ckpt = os.environ.get(
        "SAM2_CHECKPOINT",
        str(MATTE_DIR / "models" / "sam2_hiera_large.pt"),
    )
    sam2_cfg = os.environ.get("SAM2_CONFIG", "configs/sam2/sam2_hiera_l.yaml")
    print(f"loading SAM 2 cfg={sam2_cfg} ckpt={sam2_ckpt}")
    t = time.time()
    sam2_model = build_sam2(sam2_cfg, sam2_ckpt, device=device)
    predictor = SAM2ImagePredictor(sam2_model)
    print(f"sam2 loaded: {time.time()-t:.1f}s")

    # 3) Set image
    src = Image.open(src_path).convert("RGB")
    src_np = np.asarray(src)
    predictor.set_image(src_np)

    # 4) Predict
    t = time.time()
    box_arr = np.array(box, dtype=np.float32)  # XYXY
    if variant == "mask_box":
        # mask_input expects logits-shape (1, 256, 256) per SAM 2 docs.
        # Resize the binarized BiRefNet mask down to 256x256 and convert to
        # logits (10 for fg, -10 for bg) so SAM 2 treats it as confident.
        from PIL import Image as _PILImage
        seed_256 = _PILImage.fromarray((bin_seed * 255).astype("uint8"), "L").resize(
            (256, 256), _PILImage.BILINEAR
        )
        seed_logits = np.asarray(seed_256, dtype=np.float32) / 255.0
        seed_logits = (seed_logits * 20.0) - 10.0  # rough fg/bg logit
        seed_logits = seed_logits[np.newaxis, :, :]  # (1, 256, 256)
        masks, scores, _ = predictor.predict(
            box=box_arr,
            mask_input=seed_logits,
            multimask_output=False,
        )
    else:  # box_only
        masks, scores, _ = predictor.predict(
            box=box_arr,
            multimask_output=False,
        )
    print(f"predict: {time.time()-t:.1f}s, masks shape={masks.shape}, scores={scores}")

    # 5) Save RGBA
    mask = (masks[0] > 0.0).astype("uint8") * 255
    rgba = src.convert("RGBA")
    rgba.putalpha(Image.fromarray(mask, "L"))
    rgba.save(out_path, format="PNG", optimize=False)
    print(f"saved: {out_path}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
