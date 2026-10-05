# Use cases and examples

Find what you want to do, then open the page and the example folder. All example paths are in the repository under [examples/](../../examples/). Each example has its own README or header comment with run steps.

## I want to...

| Goal | Read | Example to run |
|---|---|---|
| Chat with a model right now | [CLI](cli.md#rakitsu-run) (`rakitsu run --interactive`) | none needed |
| Run one agent on one question | [agents](agents-orchestrators.md) | [single/01-chat](../../examples/single/01-chat/), [single/02-single-agent](../../examples/single/02-single-agent/) |
| Let an agent read files and run safe commands | [tools](tools.md), [security](security.md) | [single/02-single-agent](../../examples/single/02-single-agent/) |
| Have a supervisor delegate to specialists | [agents](agents-orchestrators.md#react-supervisor-with-workers) | [single/03-react-team](../../examples/single/03-react-team/) |
| Run a fixed flow (research, draft, edit) | [agents](agents-orchestrators.md#pipelines) | [single/04-pipeline](../../examples/single/04-pipeline/), [single/05-dev-team](../../examples/single/05-dev-team/) |
| Try hierarchical, plan-and-execute, loop and DAG flows | [agents](agents-orchestrators.md#pipelines) | [single/06-advanced-strategies](../../examples/single/06-advanced-strategies/) |
| Nest orchestrators (3 levels) | [agents](agents-orchestrators.md#orchestrators) | [single/07-nested-orchestrators](../../examples/single/07-nested-orchestrators/) |
| See every feature in one file | [configuration](configuration.md) | [single/08-full-featured](../../examples/single/08-full-featured/) |
| Split a config into folders | [configuration](configuration.md#file-references-and-discovery) | [modular](../../examples/modular/) (same examples, modular layout) |
| Remember facts between runs, keep long chats cheap | [memory](memory.md) | [single/09-memory-chat](../../examples/single/09-memory-chat/) |
| Fan work out to parallel subagents | [configuration](configuration.md#settingsspawn) | [single/10-spawn-fanout](../../examples/single/10-spawn-fanout/) |
| Ask about an image or audio file | [attachments](attachments.md) | [single/11-vision-chat](../../examples/single/11-vision-chat/) |
| Switch or mix providers (OpenAI, Anthropic, Gemini, Ollama, LiteLLM, NVIDIA, Codex) | [providers](providers.md) | [providers](../../examples/providers/) |
| Use the ChatGPT subscription through Codex | [providers](providers.md#codex-chatgpt-subscription) | [providers/codex.yaml](../../examples/providers/codex.yaml) |
| Talk to a voice agent that dispatches work | [chat sessions](chat-sessions-messaging.md), [A2A](a2a.md) | [single/13-voice-orchestrator](../../examples/single/13-voice-orchestrator/) |
| Watch something for days, pay only when it changes | [wake timer](wake-timer.md), [alerts](alerts.md) | [single/14-long-running-monitor](../../examples/single/14-long-running-monitor/) |
| Start a pre-approved job when an alarm fires | [start_task](start-task.md) | [single/14-long-running-monitor](../../examples/single/14-long-running-monitor/) (`btc-watcher-trigger.yaml`) |
| Run a monitor 24/7 under Docker, systemd or launchd | [monitors](monitors-healthz.md) | [deploy/monitor](../../deploy/monitor/) |
| Use rakitsu as a coding agent in Zed or another editor | [ACP](acp.md) | [acp](../../examples/acp/) |
| Let Claude Code ask a rakitsu peer (writer, reviewer) | [rakitsu-ask](rakitsu-ask.md) | the recipe on that page |
| Let two sessions or agents message each other | [chat sessions](chat-sessions-messaging.md) | `rakitsu sessions live` and `send` |
| Expose my tools to other programs over MCP | [MCP](mcp.md) | the recipe on that page |
| Let another system call my agents (A2A) | [A2A](a2a.md) | the recipe on that page |
| Check a pipeline step with a typed verification model | [tools](tools.md) (`jev` type) | [jev](../../examples/jev/) |
| Measure and compare agent setups | [examples README](../../examples/README.md) | [eval](../../examples/eval/) |
| Run a shipped agent example | [CLI](cli.md#rakitsu-run) | [single/03-react-team](../../examples/single/03-react-team/) |
| Deploy the same agent to OpenClaw or NemoClaw | [export](export.md) | `rakitsu export --format nemoclaw` |
| Start a new project without writing YAML | [scaffold and quickstart](scaffold-quickstart.md) | `rakitsu quickstart` |
| Find out why a config fails | [doctor](doctor.md) | `rakitsu doctor config.yaml` |

## A few patterns

### Cheap watcher with a smart escalation

Run free checks on a timer ([wake timer](wake-timer.md)). When a check alarms, a small model writes a short note and may start one pre-approved job ([start_task](start-task.md)). It can also send a phone push ([alerts](alerts.md)). A supervisor restarts the server and watches `/healthz` ([monitors](monitors-healthz.md)).

### Planner on one model, writer on another

Keep the planning and review in Claude Code. Send bulk writing to a rakitsu config that runs on Codex, through [rakitsu-ask](rakitsu-ask.md#cost-aware-codex-writer). Claude Code applies the text to your files, because the writer has no tools.

### An agent team behind one endpoint

Describe the team as a ReAct or pipeline orchestrator ([agents](agents-orchestrators.md)). Expose its agents with `rakitsu serve --config team.yaml`. Other systems call them over [A2A](a2a.md), and other programs can reuse your tools over [MCP](mcp.md).

### See and debug a run

Start `rakitsu serve`, run the config with `rakitsu run` (it connects by itself), and open the inspector and debugger ([serve and web UI](serve-web-ui.md)). The saved JSONL ([sessions](sessions.md)) is readable by scripts and by your editor.
