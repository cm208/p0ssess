---
description: p0ssess Pro calibration: measure how models do on samples with known answers, on the user's own OpenRouter key (use when the user asks to calibrate p0ssess, tune its routes, or see what it saves)
allowed-tools: Bash(p0ssess calibrate:*)
---

!`p0ssess calibrate plan`

Show the lines above to the user exactly as they are, in one code block, with nothing before or after it. If they are not a calibration plan (the command failed), run `p0ssess calibrate plan` with Bash and show its output the same way.

- A `next` line means the cost is within the user's auto-approve limit: run that exact command with Bash and show its output in one code block.
- An `asks` line means the cost is above that limit: ask the user in one line with the cost. Run the command it names only after they say yes.
- "up to date" or "part of p0ssess Pro" is the whole answer.

The savings report is on the local page (`/p0ssess:p0ssess-me`), and `p0ssess calibrate report --pdf FILE` writes it as a PDF. `p0ssess calibrate baseline` measures Claude alone on the same samples; it uses the user's Claude limits, so run it only when the user asks for it. `p0ssess calibrate forget` deletes every result here and on p0ssess cloud.

Never show or describe the samples' answers, and never send them anywhere: the plugin grades replies itself.
