---
description: p0ssess tour: what it does, your spending envelope, a live demo you pick and where to see spend (four steps; run it again any time)
allowed-tools: Bash(p0ssess tour:*), AskUserQuestion
---

!`p0ssess tour show`

The text above is the p0ssess tour's script. If it is not (the command failed), run `p0ssess tour show` with Bash; if that fails too, say in one line that the p0ssess plugin is not working and to check `/mcp`, and stop.

The tour is cards p0ssess draws and two pop-ups. Never paraphrase, summarize, number or add to a card: a card is shown exactly as printed, every space and box character kept, alone inside one fenced code block that starts with ```text. Write nothing else between steps unless a step says so.

1. Show CARD 1.
2. Ask QUESTION 2 with AskUserQuestion: header "Envelope", question "Your spending envelope (2 / 4): keep it?", one option per `option` line in order (label = the text before the first `|`, description = the text between the bars). Give each option except "Skip the tour" a `preview`: the card printed right under its `option` line, exactly. The first option is the recommended one.
   - An option whose command is `none`: go on.
   - "Skip the tour": run `p0ssess tour skip` with Bash, say in one line that `/p0ssess:tour` runs it again, and stop.
   - Otherwise run its command with Bash; say nothing about its output.
   - If the user types other numbers, run `p0ssess tour envelope --daily D --monthly M --task T` with them.
3. If QUESTION 3 has a `nokey` line, give that line in one sentence and go to step 4. Otherwise ask QUESTION 3 with AskUserQuestion: header "Live demo", question "Pick a live demo (3 / 4): p0ssess hands it to a cheaper model", one option per `option` line (label and description as above), each with the card under it as its `preview`. Run the chosen option's command with Bash and show its output as a card (it is one). If it fails, say in one line that the demo could not run and go on; never retry it.
4. Run `p0ssess tour done` with Bash and show its output as a card. If that card's status line says `off`, ask in one short line whether to add it, and only on a yes run `p0ssess setup statusline`. Nothing after that.
