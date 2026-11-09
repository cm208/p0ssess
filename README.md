<p align="center"><img src="assets/ghost.svg" width="120" alt=""></p>
<h1 align="center">p0ssess</h1>

Claude Code hands work to other models through OpenRouter, and stays in charge.

Early work. The agent path (a jailed OpenCode server and the relay MCP server and subagent) and the Predictive Model Engine (routing, budget, rules, history, the status line, the prompt hint and the local `/p0ssess-me` page) are built and tested; the plugin packaging is not.

- [docs/DESIGN.md](docs/DESIGN.md): the agreed design
- [docs/RESEARCH.md](docs/RESEARCH.md): how OpenRouter is used, the model and benchmark landscape, and what delegation costs
- [catalog/](catalog/): 104 use cases with stakes, routing, cost and verification
- [registry/](registry/): a dated snapshot of the models the catalog names, and scripts to rebuild the full registry
- [docs/ENGINE.md](docs/ENGINE.md): how the engine scores routes, its defaults, config files and tools
- [docs/RELAY.md](docs/RELAY.md): the agent path: jail, relay MCP tools, relay subagent, tests and known limits
- [docs/INSTALL-NOTES.md](docs/INSTALL-NOTES.md): what an install puts on a machine and how to remove it
