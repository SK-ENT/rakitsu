# Robot-cell recovery review

Rakitsu tested build: `v0.3.0-alpha.1-internal.1897` (source `b7ea2bfc`).
Requires Python 3.9+ and Rakitsu on your PATH. If needed, set
`RAKITSU_BIN` to the absolute path of the pinned binary.

A jam has stopped a fictional packaging cell. Operators need to know
which team owns the fault, whether maintenance has seen it before and
whether the affected parts need quarantine. The recovery desk prepares
one review packet from several sources.

| Stage | Source | Decision provider |
|---|---|---|
| Classify the event | Operator note | Laya |
| Check recurrence | Maintenance history | Jev |
| Flag quality review | Inspection note | OpenAI |
| Route to a team | Operator note | Gemini |
| Set disposition | Interlock and lockout facts | Fixed Python rule |

Start [Laya](https://github.com/NandhaKishorM/laya) on loopback port 8000,
then run from the extracted folder:

```sh
python3 run.py
```

By default, Laya runs live and the other three providers use simulated
loopback responses. `python3 run.py --live-cloud` uses the live cloud
providers. For that mode, load `TYPESAFE_API_KEY`, `OPENAI_API_KEY`, and
`GEMINI_API_KEY` (or `GOOGLE_API_KEY`) from your secret store before running.
Cloud providers receive the corresponding synthetic notes.

The packet is advisory. Unresolved interlock or lockout facts always
yield `HOLD`. Otherwise the packet says `MANUAL_REVIEW`. This recipe has
no robot, PLC, or actuator connection. It is a simulation and is unsuitable
as a machine safety controller.
