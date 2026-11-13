---
description: p0ssess Pro: see the plans and upgrade (use when the user asks for p0ssess Pro, upgrading, or what Pro costs)
allowed-tools: Bash(p0ssess account:*)
---

!`p0ssess account pro`

Show the lines above to the user exactly as they are, in one code block, with nothing before or after it. If they are not a p0ssess Pro summary (the command failed), run `p0ssess account pro` with Bash and show its output the same way; if that fails too, say in one line that p0ssess could not reach its cloud and to try again in a minute.

- When the output has a `Code:` line, the browser is already open and Pro turns on by itself once the user finishes there. Do not wait, poll or ask about it. If they ask later, run `p0ssess account status`.
- When the output says `Opening soon`, ask the user in one line for an email address to notify. Run `p0ssess account notify --email ADDRESS` only after they give one, and show its one line.
- When the output says they are already on Pro, that is the whole answer; the manage link is in it.
- If they want to unlink this install, `p0ssess account signout` does it.

Never ask for or handle payment details or tokens in chat. Payment happens in the browser, and the token stays in a file p0ssess manages.
