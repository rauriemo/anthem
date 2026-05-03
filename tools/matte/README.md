# Matting sidecar (`tools/matte/`)

Single Python entry point (`matte.py`) that the anthem HTTP bridge shells out
to for ML-heavy post-processing: video matting (BiRefNet keyframes + SAM 2
propagation + guided-filter edge refine) and DINOv2-based style-drift
detection. The Go processors in `internal/httpbridge/postprocess.go` invoke
this sidecar via `os/exec`; the sidecar writes a single JSON status line to
stdout and all progress logs to stderr.

Still-image matting is **not** handled here — it runs in-Go via the
`ChromaKeyProcessor` in `internal/httpbridge/postprocess.go`, which mattes
generated sprites against a uniform `#FF00FF` magenta plate deterministically
in milliseconds (no model load, no Python subprocess). That replaced the
prior BiRefNet-sidecar still-image path because Nano Banana cannot return
real transparent PNGs and the BiRefNet route both paid a model-load tax per
sprite and produced visible white halos from the off-white plate
contamination it had no spill suppression for.

Auto-detects CUDA. Falls back to CPU if torch doesn't see a usable GPU.
Degrades to per-frame BiRefNet if SAM 2 import or inference fails (same venv,
no extra install).

## Subcommands

```bash
python matte.py video \
  --frames-dir /tmp/frames_123 \
  --output-dir /tmp/frames_matted_123 \
  --keyframe-every 8 \
  --refine              # enable guided-filter edge refinement
  --radius 8            # guided-filter radius (soft vs. crisp)
  # --no-sam2           # skip SAM 2, use per-frame BiRefNet only

python matte.py drift \
  --frames-dir /tmp/frames_matted_123 \
  --anchor-threshold 0.85 \
  --window-threshold 0.92 \
  --window-size 3
```

The Go side passes every config flag explicitly; there are no implicit
defaults read from the sidecar's stdin.

## Install

1. Create a dedicated venv alongside the sidecar (this folder is gitignored):

   ```powershell
   cd tools\matte
   python -m venv .venv
   .\.venv\Scripts\Activate.ps1
   ```

   (On Bash: `python -m venv .venv && source .venv/bin/activate`.)

2. Install PyTorch matched to your CUDA toolkit first — the CUDA build has to
   come from PyTorch's extra index, not PyPI:

   ```powershell
   # CPU-only (works everywhere, slow):
   pip install torch torchvision --index-url https://download.pytorch.org/whl/cpu

   # Or CUDA 12.1 wheels (GPU acceleration):
   pip install torch torchvision --index-url https://download.pytorch.org/whl/cu121
   ```

3. Install the rest of the dependencies:

   ```powershell
   pip install -r requirements.txt
   ```

4. Install SAM 2 from source (PyPI packaging has drifted historically):

   ```powershell
   pip install "git+https://github.com/facebookresearch/segment-anything-2.git"
   ```

   If this step fails on Windows (CUDA toolchain mismatches are common), leave
   it unresolved — the video processor will transparently degrade to
   per-frame BiRefNet via the `--no-sam2` code path.

## Model checkpoints

Checkpoints live under `tools/matte/models/` (gitignored — do not commit
weights). Download once, then keep them out of git.

```powershell
cd tools\matte
mkdir models -Force

# BiRefNet — the transformers AutoModel loader will cache from HF on first
# use under ~/.cache/huggingface (no manual download needed). To pin the
# location, set BIREFNET_MODEL_ID or point HF_HOME at models\.
# Default ID: ZhengPeng7/BiRefNet.

# SAM 2 — download hiera_large checkpoint (Apache 2.0):
Invoke-WebRequest `
  -Uri "https://dl.fbaipublicfiles.com/segment_anything_2/072824/sam2_hiera_large.pt" `
  -OutFile "models\sam2_hiera_large.pt"

# DINOv2-small — loaded from HF at runtime (facebook/dinov2-small), same
# caching as BiRefNet. No manual download needed.
```

Override the checkpoint paths via environment variables if you keep models
elsewhere:

- `BIREFNET_MODEL_ID` — HuggingFace repo or local path (default `ZhengPeng7/BiRefNet`).
- `SAM2_CHECKPOINT` — path to `sam2_hiera_large.pt` (default `tools/matte/models/sam2_hiera_large.pt`).
- `SAM2_CONFIG` — SAM 2 Hydra config path (default `configs/sam2/sam2_hiera_l.yaml`; use `configs/sam2.1/sam2.1_hiera_l.yaml` if you swap in a SAM 2.1 checkpoint).
- `DINOV2_MODEL_ID` — HuggingFace repo or local path (default `facebook/dinov2-small`).
- `MATTE_PYTHON` — picked up by the Go side to locate this venv's python
  executable. Set to the absolute path of `.venv\Scripts\python.exe` on
  Windows or `.venv/bin/python` on Linux/macOS when invoking anthem.

## Verifying the install

```powershell
.\.venv\Scripts\Activate.ps1
# Materialize a few PNG frames in a scratch dir first, then:
python matte.py video --frames-dir .\test-frames --output-dir .\out --refine
```

Successful output is a single JSON line on stdout and a directory of RGBA
PNGs at `out/`. Progress and model-load diagnostics go to stderr.

## Failure modes

The Go side treats the following as safe-to-skip rather than hard failures:

- `MATTE_PYTHON` unset and no `python`/`python3` on PATH -> `PostProcessSkipped`.
- Missing `matte.py` (sidecar never installed) -> `PostProcessSkipped`.
- Non-zero exit from the sidecar with a truncated stderr tail -> `PostProcessFailed`.

For the video op specifically, a SAM 2 import or inference failure is logged
to stderr and the sidecar degrades to per-frame BiRefNet automatically (same
process, same venv, `mode` in stdout JSON flips to `birefnet_per_frame_fallback`).
This matches the plan's graceful-degradation requirement: BiRefNet per frame
is strictly better than rembg, just without temporal coherence.

## Licenses

- **BiRefNet** — MIT.
- **SAM 2** — Apache 2.0.
- **DINOv2** — Apache 2.0.
- **OpenCV / ximgproc** — Apache 2.0.

All clean for commercial game tooling. RobustVideoMatting (GPL-3.0) is
deliberately not in this stack; the sidecar's fallback is per-frame BiRefNet,
which shares the same MIT license profile and works reliably on the
stylized / creature / fantasy asset mix that RVM (trained on human portraits)
does not.
