# Calibration samples

`generate.py` rebuilds the built-in calibration samples and their answer key in
`internal/calibrate/samples/`, including `answers.toml`. Every answer is computed from the
same data that produced the file, and all random choices use fixed seeds.

    python3 scripts/calibration-samples/generate.py

Needs Python 3 with Pillow, `ffmpeg` (libmp3lame, libx264, aac), `flite` and the Liberation fonts.
Output is deterministic on one machine; a different ffmpeg or flite build may change encoded bytes.
