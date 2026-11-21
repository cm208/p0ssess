---
description: p0ssess tour: what it does, your spending envelope, a live demo you pick and where to see spend (four steps; run it again any time)
allowed-tools: Bash(p0ssess tour:*), AskUserQuestion
---

!`p0ssess tour show`

The text above is the p0ssess tour's script. If it is not (the command failed), run `p0ssess tour show` with Bash; if that fails too, say in one line that the p0ssess plugin is not working and to check `/mcp`, and stop.

The tour is cards p0ssess draws and two pop-ups. Cards come either as pop-up previews or, at the end, inside ```text code blocks. Copy each code block into your reply character for character, fences included. Never describe, summarize, translate, number or add to a card, and never put the card's content into a sentence: the user must see the box itself. Write nothing else between steps unless a step says so.

1. Start with no text at all: your first action is AskUserQuestion for QUESTION 1: header "p0ssess", question "Welcome to p0ssess. Take the one-minute tour?", one option per `option` line in order (label = the text before the first `|`, description = the text between the bars), each with the card printed right under its `option` line, exactly, as its `preview`. On "Skip the tour", run `p0ssess tour skip` with Bash, say in one line that `/p0ssess:tour` runs it again, and stop.
2. Ask QUESTION 2 with AskUserQuestion: header "Envelope", question "Your spending envelope (2 / 4): keep it?", one option per `option` line in order (label = the text before the first `|`, description = the text between the bars). Give each option except "Skip the tour" a `preview`: the card printed right under its `option` line, exactly. The first option is the recommended one.
   - An option whose command is `none`: go on.
   - "Skip the tour": run `p0ssess tour skip` with Bash, say in one line that `/p0ssess:tour` runs it again, and stop.
   - Otherwise run its command with Bash; say nothing about its output.
   - If the user types other numbers, run `p0ssess tour envelope --daily D --monthly M --task T` with them.
3. If QUESTION 3 has a `nokey` line, give that line in one sentence, run `p0ssess tour done` with Bash, and make your final reply exactly its output. Otherwise ask QUESTION 3 with AskUserQuestion: header "Live demo", question "Pick a live demo (3 / 4): p0ssess hands it to a cheaper model", one option per `option` line (label and description as above), each with the card under it as its `preview`. Run the chosen option's command with Bash (it runs the demo and finishes the tour). Never retry it.
4. Your final reply is exactly that command's whole output, character for character: the demo's code block, the farewell line as plain text, then the last code block. Do not leave it collapsed in the tool output, do not summarize it, do not add anything before or after it. Only if the last card's status line says `off`, add one short line asking whether to add it, and on a yes run `p0ssess setup statusline`.
