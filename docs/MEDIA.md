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
- Every call is authorized by the engine first (rules, lists, stakes, privacy, `.p0ssess-private`, per-task, daily and monthly limits; see ENGINE.md, Enforcement), and the ceiling (`max_cost_usd` when the caller set one, else the call's own estimate) is reserved until the measured cost settles it. Whether to ask the user is decided from the estimate. A cost the answer and `/generation` do not report is recorded at the reserved maximum, marked estimated, and replaced when a later lookup succeeds. An error answer (HTTP status other than 200) is not billed and releases the reservation.
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

Before sending, the request is priced and refused when the estimate is above `max_cost_usd`; without `max_cost_usd` the estimate is the ceiling. With no `model`, the tool picks one: it reads the use case from `use_case`, or from the files and `output` (a video: `video-understand`; audio: `audio-transcribe`; `output: image`: `image-generate`, or `image-edit` with a reference image), sizes the job from the files, and takes the engine's best direct option, preferring one that runs without asking when it costs at most half again as much. Set `dry_run` (or use `estimate`) to see the figure without a call.

The tool result is compact: the answer in `<answer>`, then `files: [{"path", "kind"}]` for generated files (kind image, video, audio or document), then one line with the model, cost and run id. In the plugin the p0ssess agent makes these calls and passes that result on (DESIGN.md, Agents).

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

## Video accuracy check

Video understanding has no public benchmark, so until 0.5.0 the engine ranked video models on the catalog's order and price, and GLM-5.3-Flash and MiMo-v2.6-flash came first. In real use MiMo described a 6-second counter clip with timestamps running to 0:21. This check replaced that guess with a measurement. Its results are in [`registry/benchmarks/p0ssess_evals.json`](../registry/benchmarks/p0ssess_evals.json), cited as "p0ssess measurement, 2026-11-09, method in docs/MEDIA.md", and the engine reads them as the board for the video understanding family ([ENGINE.md](ENGINE.md#5-p_fail)).

**Clips.** Three synthetic clips with known ground truth, made with ffmpeg and espeak-ng (640×360 H.264, AAC):

| Clip | Picture | Sound |
|---|---|---|
| counter, 8 s | white numerals 1 to 8 on navy, one per second | a 440 Hz tone from 3 s to 5 s only |
| words, 15 s | APPLE 1–3 s, BICYCLE 6–8 s, CASTLE 11–13 s on gray | espeak-ng says "The meeting moved to Thursday at nine." from 6.5 s to 9.1 s |
| cut, 12 s | a white square moving left to right on green, a hard cut at 7 s to a still black square on orange | a 1 kHz beep from 7.0 s to 7.3 s |

The ground truth was checked against 1-fps contact sheets and RMS levels of the finished files.

**Runs.** Every model got the same prompt, "Describe what happens in this video with timestamps, including what you can tell about the sound.", through `p0ssess direct run --model <id> --file <clip> --max-cost 0.05` with default settings: three clips, twice each, 42 calls. The models are the six the engine ranked first for `video-understand` on 2026-11-09 (Kimi K3.1 Pro was left out for price), plus Gemini 3.8 Flash, the catalog's recommendation.

**Scoring.** A checklist of 17 items, fixed before the runs: timestamps (7: the counter's tone at 3–5 s, each word's on and off times within 1.5 s, the speech start, the cut and the beep within 1 s, and no timestamp past the clip's end), events (6: the counter, the colors, the three words, the moving square, the cut, the second scene) and sound (4: the tone, the spoken line heard, its words, the beep). Each answer was scored by hand; a sound the model only guessed at ("would typically be a beep") does not count.

| Model | Score (of 34) | Timestamps (of 14) | Events (of 12) | Sound (of 8) | Providers that served it | Cost, 6 calls |
|---|---|---|---|---|---|---|
| google/gemini-3.8-flash | **0.88** | 12 | 12 | 6 | Google AI Studio (BYOK) | $0.0459 |
| qwen/qwen3.8-omni-flash | 0.82 | 9 | 11 | **8** | Alibaba | $0.0072 |
| google/gemini-3.1-flash-lite | 0.68 | 6 | 12 | 5 | Google AI Studio (BYOK) | $0.0031 |
| google/gemma-4-26b-a4b-it | 0.56 | 7 | 12 | 0 | Makora | $0.0013 |
| xiaomi/mimo-v2.6-flash | 0.53 | 4 | 11 | 3 | Makora (4), Xiaomi (2) | $0.0037 |
| qwen/qwen3.8-flash | 0.53 | 7 | 11 | 0 | Alibaba | $0.0045 |
| z-ai/glm-5.3-flash | 0.38 | 6 | 7 | 0 | Wafer, CoreWeave, PrimeIntellect, Reka | $0.0034 |

The check cost $0.069: $0.020 in OpenRouter credits and $0.049 billed to the attached Google key. The key's own usage counters moved by exactly the same amounts as the ledger.

### What the check found

- **Gemini 3.8 Flash was the most accurate**, as in the earlier test, with every timestamp in the counter and words clips right. It missed the 0.3-second beep both times and once added a ticking sound that is not there.
- **Qwen3.8 Omni Flash heard everything**, the beep included, and is about a sixth of Gemini's price on this account. Once it counted to 10 in an 8-second clip and called it a 10-second video.
- **MiMo's failure is its provider.** On Makora (four calls) every clip cost exactly 3,663 prompt tokens whatever its length, the timestamps ran to 0:30–0:32 for 8- and 15-second clips (the failure seen in real use), and it said it could not hear the audio. On Xiaomi's own endpoint (two calls) it scored 10 of 12, the spoken line included. OpenRouter picked the provider; p0ssess did not.
- **GLM-5.3-Flash made things up on two providers.** Served by PrimeIntellect, the cut clip became two patterned green squares; served by Reka, a Nigerian flag and a run of brand logos. On Wafer and CoreWeave its visuals were right.
- **Models without audio input cannot do this job.** Gemma 4, Qwen3.8 Flash and GLM-5.3-Flash scored 0 of 8 on sound; Gemma and GLM said every clip was silent. So `video-understand`, which already listed video and audio as its inputs, now requires a model that takes both.
- **Gemini 3.1 Flash Lite** read every picture right but described an invented ticking or beat in four of six answers.
- **Token rates differ by model.** Prompt tokens grew by 91 per second of video on both Gemini models, about 228 on the Qwen models and about 290 on GLM, and not at all on Gemma 4 or on MiMo's Makora endpoint, which take a fixed sample of frames. The engine still prices every model at 290 per second, so it overprices Gemini video about threefold (ENGINE.md, Known limits).

### What changed in the engine (0.5.1)

| Rank | 0.5.0 (catalog prior) | 0.5.1 (measured, audio required) |
|---|---|---|
| 1 | glm-5.3-flash | qwen3.8-omni-flash (p_fail 0.19) |
| 2 | gemma-4-26b-a4b-it | gemini-3.1-flash-lite (0.26) |
| 3 | mimo-v2.6-flash | mimo-v2.6-flash (0.33) |
| 4 | qwen3.8-flash | gemini-3.8-flash (0.15) |

The measured board alone was not enough: at stakes 1 a wrong pass-through answer costs a re-run plus $0.01 of harm, so with accuracy only, Gemma 4 (deaf, but cheap and right about pictures) ranked first. Requiring audio input removed it, GLM and Qwen3.8 Flash. Gemini 3.8 Flash ranks fourth on price, partly because of the token rate above.

**Limits.** Three short synthetic clips are not real footage; one person scored them; items within a clip are not independent, so the standard errors in the file understate the spread; and two runs per model cannot separate a model from the providers OpenRouter happened to pick.

## Ten minutes of video: Claude alone and with p0ssess

One real film, described twice by Claude Sonnet 5.5: once on its own, once with p0ssess. Measured on 2026-11-09.

**Clip.** *Big Buck Bunny* (© Blender Foundation, CC BY 3.0, peach.blender.org), the official 720p H.264 file from download.blender.org/peach/bigbuckbunny_movies/, remuxed to MP4 with stereo AAC (393.8 MB). The film runs 9:56.5, so the "10-minute" clip is the whole film.

**Ground truth.** Built before either run from ffmpeg contact sheets (a frame every 10 s, then every 2 to 5 s around the key scenes) and 10-second RMS levels: ten events with time windows (opening and title cards; the bunny wakes; his morning with flowers, butterflies and an apple; three rodents appear; they pelt him with fruit and nuts; they kill the butterflies; he makes weapons and traps; the ambush; the flying squirrel's glide ends over a field of stakes, caught, then flown as a kite; credits and a post-credits gag) and five sound cues (orchestral score, birdsong and nature ambience, sound effects, no dialogue, music that follows the mood at the right times). An event scores 1 if described and 1 more if its time falls within 30 s of its window; each sound cue scores 1. Maximum 25. A sound the model only guessed at ("probably") does not count.

**Runs.** Claude Code 2.1.321 in an empty directory: `claude -p "<prompt>" --model claude-sonnet-5-5 --output-format stream-json --verbose --max-turns 40 --setting-sources project,local --allowedTools Bash Read Glob Grep`, without the user's settings or plugins. Prompt: "Describe what happens in <path>/bbb-10min.mp4 with timestamps, including what you can tell about the sound." Run B adds `--plugin-dir` with p0ssess 0.5.1, a scratch `P0SSESS_HOME` (`per_task_usd = 0.50`, `plan = "api"`) and the Agent tool plus the read-only p0ssess tools. The first launch of run A failed before any turn (the prompt was placed after `--allowedTools`, which swallowed it); it was rerun once with the prompt first.

**Prices.** Sonnet 5.5 at list: $2 per million input tokens, $10 output, $0.10 cache read, $4 for a one-hour cache write (Claude Code writes the main session with a one-hour TTL). Haiku 5.5: $0.10, $0.50, $0.01, and $0.125 for a five-minute write. Totals come from each session's final `modelUsage`; its cache counts match the per-message usage in the stream, whose output counts are partial.

### What happened

- **Run A** took 5 turns and 40 s. Claude probed the file, made one 4×5 contact sheet (a frame every 30 s), measured loudness and silences with ffmpeg, and answered with a 20-row table and a loudness-based sound section. It said plainly that it could not hear and that the timestamps were approximate.
- **Run B.** The prompt hint sent Claude to the p0ssess agent. The first `direct` call failed with "attachments total 375.6 MiB, over the 48.0 MiB request limit". That error, unlike the per-video one, does not offer the `video_prep` steps, so Claude re-encoded the film itself (360p, 12 fps, 13.4 MB) and started a second agent. That call hit the 300-second limit, whose error does name the steps, and the Haiku agent chose `segment` on its own: two 300-second calls to Qwen3.8 Omni Flash, the engine's pick. 7 main turns, 5 Haiku turns, 321 s. The provider billed 13,016 completion tokens against a `max_tokens` of 8,192; the client flagged it.

| | Run A: Claude alone | Run B: Claude with p0ssess |
|---|---|---|
| Wall time | 40 s | 321 s |
| Turns | 5 | 7, plus 5 Haiku relay turns |
| Sonnet: input / cache read / cache write / output | 10 / 105,692 / 15,609 / 2,690 | 14 / 158,380 / 17,881 / 2,564 |
| Haiku: input / cache read / cache write / output | none | 10 / 23,378 / 14,275 / 5,217 |
| Claude cost at list price | $0.0999 | $0.1177 (Sonnet $0.1130, Haiku $0.0046) |
| OpenRouter cost | $0 | $0.0264 (ledger and key usage both $0.026449) |
| **Total** | **$0.0999** | **$0.1441** |
| Main context at the last call | 28,555 tokens | 30,827 tokens |
| **Score (of 25)** | **15** (events 12 of 20, sound 3 of 5) | **25** (events 20 of 20, sound 5 of 5) |

Per turn, main session (input, cache read, cache write):

| Turn | Run A | Run B |
|---|---|---|
| 1 | 2, 12,944, 6,022 | 2, 12,944, 7,805 (plugin hint and agent definitions) |
| 2 | 2, 18,966, 2,782 | 2, 20,749, 2,774 |
| 3 | 2, 21,748, 2,478 | 2, 23,523, 934 |
| 4 | 2, 24,226, 3,582 (the contact sheet) | 2, 24,457, 523 |
| 5 | 2, 27,808, 745 | 2, 24,980, 771 |
| 6 | | 2, 25,751, 225 |
| 7 | | 2, 25,976, 4,849 (the model's answer) |

Haiku relay turns in run B (input, cache read, cache write): 2, 0, 5,482; 2, 5,482, 1,729; 2, 4,943, 556; 2, 5,499, 1,955; 2, 7,454, 4,553.

**Scores.** Run A found the opening, the wake-up, the morning, the rodents, the weapon-making and the credits with the post-credits gag, all at the right times. The fruit-throwing, the butterflies' deaths, the ambush and the flying squirrel's capture fell between its 30-second frames. On sound it inferred the orchestral score from the credits, said there is no dialogue, and placed the loud stretches correctly from measured loudness; birds and impacts were guesses. Run B described every event within its window and every sound cue, with a few details wrong (it called the stakes the rodents' trap; they are the bunny's).

### Caveats

- One run each. Claude chooses its own method; a finer frame grid would catch more and cost more.
- The film is famous. Run A named the three rodents from memory, and either model may lean on what it knows of the film. A private recording would be a fairer test.
- **In this run p0ssess did not save Claude usage.** The hand-off turns, the failed first attempt and the model's 4,800-token answer in the main context cost more than run A's single contact sheet. What it bought was coverage and the soundtrack: 25 of 25 against 15, for $0.044 more in total and about 4.5 minutes more wall time.
- Run B paid for one wasted round because the 48 MiB error does not offer `video_prep`. With that hint p0ssess would downscale on its own and Claude would skip the re-encode.
- On a short clip the other model's cost is a tenth of a cent (Qwen3.8 Omni Flash averaged $0.0012 per 8–15-second clip above), so nearly all of the cost is Claude's hand-off, $0.04–0.06 on Sonnet in RESEARCH.md's measurement. Short clips save little or nothing; the reason to delegate them is the sound, which Claude cannot hear.
- Scored by hand by the agent that ran both sessions.

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
