#!/usr/bin/env python3
"""Matting sidecar for the Anthem HTTP bridge post-process pipeline.

Subcommands:
  video - video sequence matting via BiRefNet keyframes + SAM 2 propagation
          + guided-filter edge refinement. Degrades to per-frame BiRefNet
          when --no-sam2 is passed or SAM 2 fails at runtime.
  drift - DINOv2 embedding-based style-drift detection per frame, with
          both anchor (vs. frame 0) and rolling-window similarity scores.

Still-image matting is no longer handled here — it now runs in-Go via the
ChromaKeyProcessor in internal/httpbridge/postprocess.go (deterministic
magenta-plate keying, no model load, sub-second).

Each subcommand writes a single structured JSON status line to stdout; all
progress logging goes to stderr so Go-side parsing stays clean.

Designed to be called from Go via exec.Cmd. Auto-detects CUDA; falls back
to CPU. Go side does not need to know which device was used.
"""

from __future__ import annotations

import argparse
import json
import os
import sys
import time
import traceback
from pathlib import Path
from typing import Any


def _log(msg: str) -> None:
    """Progress/diagnostic line on stderr so stdout stays JSON-clean."""
    print(msg, file=sys.stderr, flush=True)


def _emit(payload: dict[str, Any]) -> None:
    """Write a single JSON object to stdout (Go parses the last line)."""
    sys.stdout.write(json.dumps(payload, separators=(",", ":")) + "\n")
    sys.stdout.flush()


def _device() -> str:
    try:
        import torch  # type: ignore

        return "cuda" if torch.cuda.is_available() else "cpu"
    except Exception:
        return "cpu"


_FRAME_EXTS = (".png", ".jpg", ".jpeg")


def _list_frames(frames_dir: Path) -> list[Path]:
    """Accepts PNG or JPEG inputs.

    anthem's `extract_video_frames` post-process op currently writes PNG (ffmpeg
    default for `%05d.png`). SAM 2 exclusively scans for JPEG frames in
    `init_state`, so `_sam2_propagate` materializes a JPEG mirror when needed;
    we still want `_list_frames` itself to cover both so the same matte.py
    binary handles both anthem-invoked and manual/smoke-test inputs."""
    return sorted(
        p for p in frames_dir.iterdir() if p.suffix.lower() in _FRAME_EXTS
    )


# ---------------------------------------------------------------------------
# BiRefNet loader (cached across calls within a single process)
# ---------------------------------------------------------------------------


_BIREFNET_MODEL = None


def _load_birefnet(device: str):
    """Load BiRefNet once per process. Uses the HuggingFace transformers
    pipeline / AutoModel path to avoid an additional pip package."""
    global _BIREFNET_MODEL
    if _BIREFNET_MODEL is not None:
        return _BIREFNET_MODEL

    from transformers import AutoModelForImageSegmentation  # type: ignore
    import torch  # type: ignore

    model_id = os.environ.get("BIREFNET_MODEL_ID", "ZhengPeng7/BiRefNet")
    _log(f"[birefnet] loading {model_id} on {device}")
    model = AutoModelForImageSegmentation.from_pretrained(
        model_id, trust_remote_code=True
    )
    model.to(device)
    model.eval()
    if device == "cuda":
        try:
            model.half()
        except Exception:
            pass
    _BIREFNET_MODEL = (model, torch, device)
    return _BIREFNET_MODEL


def _birefnet_infer(image_path: Path, device: str):
    """Return an RGBA PIL.Image with BiRefNet alpha applied to the source."""
    from PIL import Image  # type: ignore
    from torchvision import transforms  # type: ignore
    import numpy as np  # type: ignore

    model, torch, device = _load_birefnet(device)

    src = Image.open(image_path).convert("RGB")
    W, H = src.size

    target = int(os.environ.get("BIREFNET_INPUT_SIZE", "1024"))
    preproc = transforms.Compose(
        [
            transforms.Resize((target, target)),
            transforms.ToTensor(),
            transforms.Normalize(
                [0.485, 0.456, 0.406],
                [0.229, 0.224, 0.225],
            ),
        ]
    )
    inp = preproc(src).unsqueeze(0).to(device)
    if device == "cuda":
        inp = inp.half()

    with torch.no_grad():
        preds = model(inp)[-1].sigmoid().cpu()
    mask = preds[0].squeeze().float().numpy()

    mask_img = Image.fromarray((mask * 255).astype("uint8"), mode="L").resize(
        (W, H), Image.BILINEAR
    )

    rgba = src.convert("RGBA")
    rgba.putalpha(mask_img)
    return rgba, np.asarray(mask_img)


# ---------------------------------------------------------------------------
# video subcommand
# ---------------------------------------------------------------------------


def _guided_filter_refine(rgb: "np.ndarray", alpha: "np.ndarray", radius: int):
    """Soft-edge recovery via OpenCV ximgproc guided filter, falling back to
    a no-op if ximgproc isn't available."""
    try:
        import cv2  # type: ignore

        if hasattr(cv2, "ximgproc") and hasattr(cv2.ximgproc, "guidedFilter"):
            guide = rgb.astype("float32") / 255.0
            a = alpha.astype("float32") / 255.0
            refined = cv2.ximgproc.guidedFilter(
                guide, a, radius, eps=1e-3
            )
            import numpy as np  # type: ignore

            return (np.clip(refined, 0.0, 1.0) * 255.0).astype("uint8")
    except Exception as e:
        _log(f"[video] guided filter skipped: {e}")
    return alpha


def _per_frame_birefnet(
    frames: list[Path], output_dir: Path, device: str, refine: bool, radius: int
) -> tuple[int, int]:
    """Fallback path: BiRefNet per frame, no temporal propagation."""
    from PIL import Image  # type: ignore
    import numpy as np  # type: ignore

    done = 0
    for fp in frames:
        rgba, mask = _birefnet_infer(fp, device)
        if refine:
            rgb = np.asarray(rgba.convert("RGB"))
            mask = _guided_filter_refine(rgb, mask, radius)
            rgba = Image.fromarray(rgb, "RGB").convert("RGBA")
            rgba.putalpha(Image.fromarray(mask, "L"))
        out = output_dir / (fp.stem + ".png")
        rgba.save(out, format="PNG", optimize=False)
        done += 1
        if done % 8 == 0:
            _log(f"[video] per-frame BiRefNet {done}/{len(frames)}")
    return done, 0


def _sam2_propagate(
    frames: list[Path],
    output_dir: Path,
    device: str,
    keyframe_every: int,
    refine: bool,
    radius: int,
) -> tuple[int, int]:
    """Primary path: BiRefNet on keyframes + SAM 2 video propagation for
    every frame + optional guided-filter edge refinement.

    Raises on SAM 2 import or inference failure so the caller can degrade."""
    from PIL import Image  # type: ignore
    import numpy as np  # type: ignore

    from sam2.build_sam import build_sam2_video_predictor  # type: ignore

    sam2_ckpt = os.environ.get(
        "SAM2_CHECKPOINT",
        str(Path(__file__).resolve().parent / "models" / "sam2_hiera_large.pt"),
    )
    sam2_cfg = os.environ.get(
        "SAM2_CONFIG",
        "configs/sam2/sam2_hiera_l.yaml",
    )
    _log(f"[video] loading SAM 2 cfg={sam2_cfg} ckpt={sam2_ckpt}")
    predictor = build_sam2_video_predictor(sam2_cfg, sam2_ckpt, device=device)

    # SAM 2's init_state scans the directory for .jpg/.jpeg files only
    # (sam2/utils/misc.py::load_video_frames_from_jpg_images). If we were
    # handed PNG frames — which is anthem's extract_video_frames default —
    # mirror them to a sibling scratch dir as JPEGs so SAM 2 can ingest them.
    # Using JPEG here is fine: SAM 2 only uses these as RGB inputs for its
    # vision encoder; the alpha / matting output reads from the originals.
    frames_dir = frames[0].parent
    sam2_dir = frames_dir
    jpeg_like = [p for p in frames if p.suffix.lower() in (".jpg", ".jpeg")]
    if len(jpeg_like) != len(frames):
        from PIL import Image  # type: ignore  (already imported above; cheap re-import)
        sam2_dir = frames_dir / "_sam2_jpeg_mirror"
        sam2_dir.mkdir(exist_ok=True)
        for idx, fp in enumerate(frames):
            dst = sam2_dir / f"{idx:05d}.jpg"
            if dst.exists():
                continue
            Image.open(fp).convert("RGB").save(dst, format="JPEG", quality=95)
        _log(f"[video] materialized {len(frames)} JPEG mirror frames for SAM 2 at {sam2_dir}")

    state = predictor.init_state(video_path=str(sam2_dir))

    # Seed from BiRefNet masks at keyframes.
    keyframe_indices = list(range(0, len(frames), max(1, keyframe_every)))
    if (len(frames) - 1) not in keyframe_indices:
        keyframe_indices.append(len(frames) - 1)

    for idx in keyframe_indices:
        _, seed_mask = _birefnet_infer(frames[idx], device)
        bin_mask = (seed_mask > 127).astype("uint8")
        predictor.add_new_mask(
            state,
            frame_idx=idx,
            obj_id=1,
            mask=bin_mask,
        )

    masks_by_frame: dict[int, "np.ndarray"] = {}
    first_mask_logged = False
    for frame_idx, _, logits in predictor.propagate_in_video(state):
        # SAM 2.1 yields out_mask_logits of shape (num_obj, 1, H, W); older
        # builds sometimes use (num_obj, H, W). Select object 0 and squeeze
        # every leading singleton dim so we always end up with a 2-D mask.
        raw = logits[0].detach().cpu().numpy()
        m = (raw > 0.0)
        while m.ndim > 2 and m.shape[0] == 1:
            m = m[0]
        if not first_mask_logged:
            _log(
                f"[video] sam2 propagate: logits shape={tuple(logits.shape)} "
                f"-> mask shape={m.shape}"
            )
            first_mask_logged = True
        if m.ndim != 2:
            raise RuntimeError(
                f"unexpected SAM 2 mask rank {m.ndim}, shape {m.shape}"
            )
        masks_by_frame[frame_idx] = (m.astype("uint8") * 255)

    done = 0
    for idx, fp in enumerate(frames):
        mask = masks_by_frame.get(idx)
        if mask is None:
            continue
        src = Image.open(fp).convert("RGB")
        src_W, src_H = src.size
        # SAM 2's internal processing may return masks at a resolution that
        # differs from the source frame (e.g. 1024x1024 when the encoder
        # pre-resizes). Normalize to source resolution before the alpha
        # composite so the guided filter and putalpha both see matched dims.
        if mask.shape != (src_H, src_W):
            mask = np.asarray(
                Image.fromarray(mask, "L").resize(
                    (src_W, src_H), Image.BILINEAR
                )
            )
        if refine:
            rgb = np.asarray(src)
            mask = _guided_filter_refine(rgb, mask, radius)
        rgba = src.convert("RGBA")
        rgba.putalpha(Image.fromarray(mask, "L"))
        out = output_dir / (fp.stem + ".png")
        rgba.save(out, format="PNG", optimize=False)
        done += 1
        if done % 16 == 0:
            _log(f"[video] sam2 export {done}/{len(frames)}")

    return done, len(keyframe_indices)


def cmd_video(args: argparse.Namespace) -> int:
    device = _device()
    started = time.time()

    frames_dir = Path(args.frames_dir)
    output_dir = Path(args.output_dir)
    if not frames_dir.is_dir():
        _emit({"status": "error", "error": f"frames_dir not found: {frames_dir}"})
        return 2

    output_dir.mkdir(parents=True, exist_ok=True)

    frames = _list_frames(frames_dir)
    if not frames:
        _emit({"status": "error", "error": f"no PNG/JPEG frames in {frames_dir}"})
        return 2

    mode = "sam2"
    processed = 0
    keyframes = 0
    if args.no_sam2:
        mode = "birefnet_per_frame"
        processed, keyframes = _per_frame_birefnet(
            frames, output_dir, device, refine=args.refine, radius=args.radius
        )
    else:
        try:
            processed, keyframes = _sam2_propagate(
                frames,
                output_dir,
                device,
                keyframe_every=max(1, args.keyframe_every),
                refine=args.refine,
                radius=args.radius,
            )
        except Exception as e:
            _log(f"[video] SAM 2 path failed, degrading to per-frame BiRefNet: {e}")
            _log(traceback.format_exc())
            mode = "birefnet_per_frame_fallback"
            processed, keyframes = _per_frame_birefnet(
                frames,
                output_dir,
                device,
                refine=args.refine,
                radius=args.radius,
            )

    _emit(
        {
            "status": "ok",
            "mode": mode,
            "device": device,
            "frames_processed": processed,
            "keyframes": keyframes,
            "elapsed_ms": int((time.time() - started) * 1000),
            "frames_dir": str(frames_dir),
            "output_dir": str(output_dir),
        }
    )
    return 0


# ---------------------------------------------------------------------------
# drift subcommand
# ---------------------------------------------------------------------------


_DINO_MODEL = None


def _load_dino(device: str):
    global _DINO_MODEL
    if _DINO_MODEL is not None:
        return _DINO_MODEL

    from transformers import AutoImageProcessor, AutoModel  # type: ignore
    import torch  # type: ignore

    model_id = os.environ.get("DINOV2_MODEL_ID", "facebook/dinov2-small")
    _log(f"[drift] loading {model_id} on {device}")
    processor = AutoImageProcessor.from_pretrained(model_id)
    model = AutoModel.from_pretrained(model_id).to(device).eval()
    _DINO_MODEL = (processor, model, torch, device)
    return _DINO_MODEL


def _embed_frame(processor, model, torch, device, path: Path):
    from PIL import Image  # type: ignore

    img = Image.open(path).convert("RGB")
    inputs = processor(images=img, return_tensors="pt").to(device)
    with torch.no_grad():
        out = model(**inputs)
    # Use CLS token (first position) from last_hidden_state for a stable embedding.
    emb = out.last_hidden_state[:, 0, :]
    emb = emb / emb.norm(dim=-1, keepdim=True).clamp_min(1e-9)
    return emb.squeeze(0).cpu().numpy()


def cmd_drift(args: argparse.Namespace) -> int:
    device = _device()
    started = time.time()

    frames_dir = Path(args.frames_dir)
    if not frames_dir.is_dir():
        _emit({"status": "error", "error": f"frames_dir not found: {frames_dir}"})
        return 2

    frames = _list_frames(frames_dir)
    if not frames:
        _emit({"status": "error", "error": f"no PNG/JPEG frames in {frames_dir}"})
        return 2

    try:
        import numpy as np  # type: ignore

        processor, model, torch, device = _load_dino(device)
        embs = []
        for i, fp in enumerate(frames):
            embs.append(_embed_frame(processor, model, torch, device, fp))
            if (i + 1) % 16 == 0:
                _log(f"[drift] embedded {i + 1}/{len(frames)}")
        embs_arr = np.stack(embs, axis=0)
    except Exception as e:
        _log(traceback.format_exc())
        _emit({"status": "error", "error": f"dinov2 embedding failed: {e}"})
        return 1

    anchor = embs_arr[0]
    anchor_scores = (embs_arr @ anchor).tolist()

    window_size = max(1, args.window_size)
    window_scores: list[float] = []
    for i in range(len(embs_arr)):
        lo = max(0, i - window_size)
        hi = min(len(embs_arr), i + window_size + 1)
        neighbors = [embs_arr[j] for j in range(lo, hi) if j != i]
        if not neighbors:
            window_scores.append(1.0)
            continue
        mean = sum(neighbors) / len(neighbors)
        norm = float((mean @ mean) ** 0.5) or 1.0
        mean = mean / norm
        window_scores.append(float(embs_arr[i] @ mean))

    anchor_outliers = [i for i, s in enumerate(anchor_scores) if s < args.anchor_threshold]
    window_outliers = [i for i, s in enumerate(window_scores) if s < args.window_threshold]

    _emit(
        {
            "status": "ok",
            "device": device,
            "frames_processed": len(frames),
            "anchor_threshold": args.anchor_threshold,
            "window_threshold": args.window_threshold,
            "window_size": window_size,
            "anchor_scores": anchor_scores,
            "window_scores": window_scores,
            "anchor_outliers": anchor_outliers,
            "window_outliers": window_outliers,
            "elapsed_ms": int((time.time() - started) * 1000),
        }
    )
    return 0


# ---------------------------------------------------------------------------
# entry point
# ---------------------------------------------------------------------------


def build_parser() -> argparse.ArgumentParser:
    p = argparse.ArgumentParser(prog="matte.py")
    sub = p.add_subparsers(dest="command", required=True)

    pv = sub.add_parser("video", help="video matting via BiRefNet + SAM 2")
    pv.add_argument("--frames-dir", required=True)
    pv.add_argument("--output-dir", required=True)
    pv.add_argument("--keyframe-every", type=int, default=8)
    pv.add_argument("--refine", action="store_true")
    pv.add_argument("--radius", type=int, default=8)
    pv.add_argument(
        "--no-sam2",
        action="store_true",
        help="skip SAM 2 propagation and do per-frame BiRefNet only",
    )

    pd = sub.add_parser("drift", help="DINOv2 style-drift check")
    pd.add_argument("--frames-dir", required=True)
    pd.add_argument("--anchor-threshold", type=float, default=0.85)
    pd.add_argument("--window-threshold", type=float, default=0.92)
    pd.add_argument("--window-size", type=int, default=3)

    return p


def main(argv: list[str] | None = None) -> int:
    args = build_parser().parse_args(argv)
    try:
        if args.command == "video":
            return cmd_video(args)
        if args.command == "drift":
            return cmd_drift(args)
    except Exception as e:
        _log(traceback.format_exc())
        _emit({"status": "error", "error": str(e)})
        return 1
    return 2


if __name__ == "__main__":
    sys.exit(main())
