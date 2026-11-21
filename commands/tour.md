---
description: p0ssess tour: what it does, your spending envelope, one live demo route and where to see spend (four short steps; run it again any time)
allowed-tools: Bash(p0ssess tour:*), AskUserQuestion
---

!`p0ssess tour show`

The lines above are the p0ssess tour for this install. If they are not (the command failed), run `p0ssess tour show` with Bash; if that fails too, say in one line that the p0ssess plugin is not working and to check `/mcp`, and stop.

Give it as four short steps, in order, numbered 1 to 4, one or two plain lines each, each step starting on its own line with its number; never fold one step's text into another. No headings, no tables, no extra advice.

1. The `what` line, as written.
2. The `envelope` line: the daily limit, the monthly cap and the per-task auto-approve, in one line. Then ask once with AskUserQuestion: one question ("Keep this spending envelope?"), options in this order: each `choice` line (its label, with its numbers as the description; the first is the recommended one), then "Skip the tour". On a choice with a command, run that command with Bash and say its `saved` line in a few words; on "no change", go on. On "Skip the tour", run `p0ssess tour skip` with Bash, say in one line that `/p0ssess:tour` runs it later, and stop. If the user names other numbers, run `p0ssess tour envelope --daily D --monthly M --task T` with them.
3. The live demo: say first, in one line, "3. Live demo: p0ssess hands a small task to a cheaper model." Then run `p0ssess tour demo` with Bash and show its result in exactly this shape, nothing else:
   `Asked <model> to <asked line>:` on one line, the `answer` line as a quote (`> answer`) on the next, then the `receipt` line in inline code and its `cost` in a few words on a third line. If it prints `skip`, give that line instead. If it fails, say in one line that the demo could not run and go on; never retry it.
4. The `see` line, in plain words. If it says the status line is not set up, offer `p0ssess setup statusline` in one line and run it only after the user says yes.

Then run `p0ssess tour done` with Bash and end with nothing more than one line: limits change any time in plain words ("cap p0ssess at $5 a month").
