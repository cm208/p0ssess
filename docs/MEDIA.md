# Media on the direct path

The direct path sends a one-shot job to one OpenRouter model in a single call: a prompt plus local files (images, PDFs, audio, video, text), and back come text, a transcript, or generated images saved to disk. It is how p0ssess handles media that OpenCode 2.2.5 drops (audio and video attachments never reach the model there), and anything else that does not need an agent loop.

Code: [`internal/direct`](../internal/direct). Shell entry point: `p0ssess direct`. MCP tool: `direct` (schema in `internal/direct/tool.go`).

Status: built and verified with real calls on 2026-11-09. All numbers below come from those calls.

## Calling it

```sh
p0ssess direct estimate --model google/gemini-3.8-flash --prompt "Describe this video" --file clip.mp4
p0ssess direct run --model google/gemini-3.8-flash --prompt "Describe this video" --file clip.mp4 --max-cost 0.03
p0ssess direct run --model openai/whisper-large-v3-turbo --output transcript --timestamps --file talk.mp3 --max-cost 0.01
p0ssess direct run --model recraft/recraft-v4.1-flash --output image --prompt "a ghost icon" --max-cost 0.02
```

The key is read from `$P0SSESS_OPENROUTER_KEY_FILE` or `~/.config/p0ssess/secrets/openrouter_key` and is never printed. The answer comes first, inside `<answer>`, then output files, then one line with model, provider, actual cost and the estimate, then `run: d-…`, the id of the measured run in the engine's ledger (see [ENGINE.md](ENGINE.md#the-ledger)).

| Output | Route | Notes |
|---|---|---|
| `text` (default), `json` | `/chat/completions` | `json` adds `response_format: json_object` |
| `image` | `/chat/completions` with `modalities: ["image","text"]` for models that also output text (Gemini image models); `/images` for image-only models | images are decoded and written to the output folder; paths and MIME types are returned |
| `transcript` | `/audio/transcriptions` | `--timestamps` asks for segments and prints `[m:ss–m:ss] text` lines |

Attachments are sent as OpenRouter documents them for Chat Completions: images as `image_url` data URLs, PDFs as `file` parts with the `file-parser` plugin, audio as `input_audio` (base64 plus a format string), video as `video_url` data URLs (`video/mp4`, `video/mpeg`, `video/mov`, `video/webm`), with Gemini's optional `processing: agentic|static`.

PDF engine, unless the caller picks one: `native` when the model takes files, `mistral-ocr` for PDFs without a text layer, and the free `cloudflare-ai` parser otherwise.

## Privacy

- Every call sends `provider.data_collection: "deny"`. `--zdr` (`strict_zdr`) adds `provider.zdr: true`, and so does every call when the budget's privacy mode is `zdr`.
- Every call is authorized by the engine first (rules, lists, stakes, privacy, `.p0ssess-private`, per-task, daily and monthly limits; see ENGINE.md, Enforcement), and `max_cost_usd` is reserved until the measured cost settles it. A cost the answer and `/generation` do not report is recorded at the reserved maximum, marked estimated, and replaced when a later lookup succeeds. An error answer (HTTP status other than 200) is not billed and releases the reservation.
- With `zdr: true`, OpenRouter routes to zero-retention endpoints, or to Safety Retention endpoints when the account's privacy settings allow them. On the test account a Gemini 3.8 Flash call with `zdr: true` succeeded through a "Google" endpoint, although the registry lists no ZDR endpoint for Gemini. So strict mode means "ZDR or what the account allows", not "never Gemini".
- The image API (`/images`) documents only `only`, `order`, `ignore`, `sort` and `allow_fallbacks` as routing fields. p0ssess sends `data_collection: "deny"` there too, and OpenRouter accepted it without error, but it is not documented to be honored. Under strict ZDR, image-only models are refused; use an image model served on chat completions.
- Files are checked before anything is read for upload:
  - names that look like secrets are refused: `.env` and `*.env`, `*.pem`, `*.p12`, `*.pfx`, `*.kdbx`, anything containing `key`, `secret` or `credential`, `id_*`, `.netrc`, `.pgpass`, `.npmrc`, `.pypirc`, `htpasswd`
  - files inside `.ssh`, `.gnupg`, `.aws`, `.kube`, `.docker` or `secrets` directories are refused
  - symlinks are resolved and both names are checked
  - a `.p0ssess-private` file anywhere from the file's folder up to its repository root (or up to `/` outside a repository) blocks the file
- Only local paths are accepted; nothing is fetched from URLs.

## Limits and large videos

| Input | Default limit | When over |
|---|---|---|
| Image | 20 MiB | refused, with a resize hint |
| PDF | 32 MiB | refused, with a split hint |
| Audio | 25 MiB (the transcription endpoint's multipart cap) | refused, with a re-encode hint |
| Video | 20 MiB inline or 300 s | refused unless the caller opts into a local ffmpeg step |
| All attachments | 48 MiB per request | refused |

Nothing large is uploaded silently. A video over the limit returns an error naming three opt-in steps (`--video-prep`):

- `downscale`: re-encode locally to 360p, 1 fps, mono 32 kbit/s audio. Gemini samples video at 1 fps by default, so little is lost for most questions.
- `keyframes`: scene-change frames (first frame plus every cut, up to 24) sent as images labeled with their timestamps, plus the audio track as MP3 when the model takes audio. Cheapest; loses motion between cuts.
- `segment`: split into 300 s parts and run one call per part, each told its place in the full video; answers are joined under `[segment i of n, start to end]` headers. Spending is checked after each part.

## Cost guard

Before sending, the request is priced and refused when the estimate is above `max_cost_usd`. Set `dry_run` (or use `estimate`) to see the figure without a call.

- **Prices** come from OpenRouter's public endpoint listings (no key needed) and are the **highest across endpoints**; the registry snapshot is the offline fallback. Chat calls also send `provider.max_price` with the same prices, so routing cannot pick a pricier endpoint than the estimate assumed. An earlier version used the model-level list price instead: one DeepSeek call landed on an endpoint above it (actual $0.001213 against an estimate of $0.001187), and a GLM-5.3-Flash call failed outright with "No endpoints found that satisfy the max price", because every endpoint that fit was above the model-level price.
- **Token rates**: text at 3 bytes per token; 1,600 tokens per image; PDFs at 600 tokens per page on Gemini and 3,000 elsewhere when native, 1,000 per page when parsed, plus $0.002 per page for `mistral-ocr`; audio at 32 tokens per second on Gemini and 50 elsewhere; video at 300 tokens per second on Gemini and 600 elsewhere. Durations come from ffprobe (or the WAV header, or a low-bitrate guess from file size, which is flagged).
- **Unknown prices**: when a price the call needs is not known (no token price for a chat call, no image price for image output, no per-second price for transcription) and the model is not free (a `:free` variant, or a text model the snapshot lists at zero), the estimate is reported as unknown, not $0. The call needs `max_cost_usd` above 0, which is reserved and counted in full; no `max_price` is sent, so the measured cost settles it. A live listing without an image price keeps the snapshot's.
- **Output** is priced at the full `max_tokens` (default 4,096) at the higher of the completion and reasoning prices.
- **Image generation** uses per-image endpoint prices where listed, else 2 MP for per-megapixel prices, else 2,000 image tokens on Gemini and 4,200 elsewhere.
- **Transcription** is priced per second of audio (confirmed below).
- After the call, the result warns when the actual cost was above the estimate or when the provider billed more completion tokens than `max_tokens`.

**Bring-your-own-key calls.** On an account with a provider key attached, OpenRouter's `usage.cost` is $0 and the provider bills its own rates to that key; OpenRouter reports them in `usage.cost_details.upstream_inference_cost` with `is_byok: true`. p0ssess counts both: `cost_usd` is the OpenRouter charge plus the upstream charge, and both parts are shown. The upstream rates can be higher than OpenRouter's list price for the same model. On the test account, Gemini 3.8 Flash on Google AI Studio billed about $1.50 per million prompt tokens and $7.50 per million completion tokens, against the $0.825 and $4.125 listed, so BYOK calls can exceed the estimate.

## Live results, 2026-11-09

Synthetic media made with ffmpeg and espeak-ng:

- video: 10 s, 640×360, red for 5 s then blue, the white title "TEST CARD 42", a yellow `t=Ns` counter, a constant 440 Hz tone (108 KB MP4)
- audio: 16.2 s of synthetic speech, three sentences with a day, a time and two items (98 KB MP3)
- PDF: one page, text layer, a 4-column table with a total row (1.5 KB)
- image: 512×384 PNG, a blue square, a gold circle and a caption line

"BYOK" marks calls billed to the provider key attached to the test account; their cost is the upstream figure.

| Job | Model (provider) | Result | Cost | Estimate | Tokens in / out (reasoning) |
|---|---|---|---|---|---|
| Describe video | google/gemini-3.8-flash (Google AI Studio, BYOK) | Correct: title, counter 0–9 s, red to blue at 5 s, constant tone | $0.006684 | $0.0058 | 936 / 704 (481); 910 video tokens |
| Describe video | qwen/qwen3.8-omni-flash (Alibaba) | Visuals right; invented a "red outline" and a pitch change at 5 s | $0.001775 | $0.0013 | 2,352 / 3,027 (2,714) with `max_tokens` 800 |
| Describe video | xiaomi/mimo-v2.6-flash (Makora, ZDR) | Visuals right; said the video was silent | $0.000549 | $0.0011 | 3,669 / 128 |
| Video as keyframes + audio, strict ZDR | xiaomi/mimo-v2.6-flash (Makora) | 2 frames + MP3; right colors and times; called the tone 1 kHz | $0.000105 | $0.0070 | 575 / 88 |
| Transcribe and extract | google/gemini-3.8-flash (BYOK) | Word-perfect; day, time and items listed | $0.001605 | $0.0038 | 425 / 129; 406 audio tokens |
| Transcribe and extract | xiaomi/mimo-v2.6-flash (Makora) | One word wrong ("short at recording"); extraction right | $0.000049 | $0.0003 | 131 / 109 |
| Transcript with timestamps | openai/whisper-large-v3-turbo | Word-perfect, 5 timed segments | $0.000180 | $0.0002 | 16.2 s billed |
| Transcript, strict ZDR | openai/whisper-large-v3-turbo | Word-perfect | $0.000054 | $0.0002 | 16.2 s billed |
| PDF table to JSON, `cloudflare-ai` | deepseek/deepseek-v4.1-flash (DeepInfra) | **Empty**: reasoning used all 800 tokens | $0.000363 | $0.0005 | 195 / 800 (800) |
| Same, `reasoning: low`, 2,000 tokens | deepseek/deepseek-v4.1-flash (Relace) | Correct JSON, numbers as numbers | $0.000729 | $0.0012 | 195 / 1,456 (1,119) |
| PDF table to JSON, `mistral-ocr` | deepseek/deepseek-v4.1-flash (DeepInfra) | Correct JSON | $0.002204 | $0.0032 | 179 / 426 (311); includes $0.002 OCR |
| PDF table to JSON, `native` | google/gemini-3.8-flash (BYOK) | Correct JSON | $0.002487 | $0.0038 | 558 / 220 |
| Image alt text | google/gemini-3.8-flash (BYOK) | Correct | $0.002427 | $0.0030 | 1,083 / 107 |
| Image alt text, strict ZDR | google/gemini-3.8-flash ("Google") | Correct | $0.001313 | $0.0035 | 1,070 / 136 |
| Image alt text | z-ai/glm-5.3-flash (Friendli) | Correct | $0.000098 | $0.0008 | 298 / 106 |
| Generate image (image API) | recraft/recraft-v4.1-flash | 1024×1024 WebP, on prompt | $0.007000 | $0.0070 | 4,175 image tokens |
| Generate image (chat) | google/gemini-3.1-flash-lite-image (BYOK) | 1024×1024 JPEG, on prompt | $0.034088 | $0.0660 | 18 / 1,442 |

The gated live test set (`P0SSESS_LIVE=1 go test -run TestLive ./internal/direct`) then ran video, audio, image caption, PDF and image generation once more: all passed, $0.015181 in total ($0.007352 on OpenRouter and $0.007829 BYOK).

**Total for this verification:** $0.078 ($0.023 in OpenRouter credits, $0.055 billed to the attached provider key).

### What the numbers say

- **Gemini 3.8 Flash is the reliable default for video and audio understanding.** It was the only model that got every detail of the test video right, including the audio track. Measured rates were lower than the published upper ends: 910 tokens for 10 s of video (about 91 per second at default media resolution, against the 300 the estimate assumes) and 406 tokens for 16.2 s of audio (about 25 per second, against 32). A one-page PDF cost 558 prompt tokens natively.
- **The cheap omni models are good for visuals and speech, not for judging audio.** Qwen3.8 Omni Flash invented a pitch change; MiMo V2.6 Flash missed the audio inside an MP4 entirely but transcribed a separate MP3 nearly perfectly. For "what is in this sound" use Gemini; for cheap speech-to-text use a transcription model.
- **Transcription models are the cheapest route for speech**, about $0.0007 per minute on the endpoint that served it. The price OpenRouter lists without a unit is **USD per second of audio**: $0.000180 for 16.2 s is exactly 16.2 s × $0.0000111 (the Groq endpoint's listed price), and the strict-ZDR call matched 16.2 s × $0.00000333 (DeepInfra's).
- **Reasoning models need room or a lower effort.** DeepSeek V4.1 Flash spent all 800 tokens reasoning and returned nothing; at `reasoning: low` it still reasoned for 1,119–1,874 tokens. Qwen3.8 Omni Flash billed 3,027 completion tokens against a `max_tokens` of 800, so that cap is not a hard limit on every provider; the client warns when it happens.
- **Keyframes are the cheapest way to "watch" a video** when motion between cuts does not matter: $0.000105 for two frames plus the audio track on a ZDR endpoint.
- **Image generation** cost exactly the listed per-image price on the image API ($0.007). The Gemini chat route billed 1,442 completion tokens for one image; the estimate's 2,000-token allowance covered it.

## Local shortcuts

Some jobs need no model at all, or a much smaller one. These run ffmpeg locally and are exported for the engine to choose:

| Helper | Command | Use |
|---|---|---|
| `Probe` | `p0ssess direct probe FILE` | duration, size and streams before deciding anything |
| `FrameAt` | `p0ssess direct frame FILE --at S --out f.jpg` | read an on-screen timestamp or error with Claude's own vision (catalog `video-timestamp-ocr`) |
| `SceneFrames` | `p0ssess direct frames FILE --scenes --out DIR` | structure of a screen recording without a model (catalog `video-structure-scan`) |
| `FramesEvery` | `p0ssess direct frames FILE --every 5 --out DIR` | evenly spaced frames |
| `Downscale` | `p0ssess direct downscale FILE --out small.mp4` | shrink before upload |
| `SplitVideo` | `p0ssess direct split FILE --out DIR --seconds 300` | segment long recordings |
| `ExtractAudio` | `p0ssess direct audio FILE --out a.mp3` | send only the soundtrack to a transcription model |

## Not covered yet

Text-to-speech (`/audio/speech`), audio output from chat models, video generation (`/videos`, asynchronous), embeddings and rerank. They use other routes and billing units and are not built yet. Reranking stays off until OpenRouter exposes a price (see RESEARCH.md).
