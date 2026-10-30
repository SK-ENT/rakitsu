| widget/mode | step | caption | active node | lit edge(s) | counters |
|---|---|---|---|---|---|
| hero | 0 | Think: the model reads the task and the history so far, then decides what to do next. | Think | Think -> Act [tool_call] |  |
| hero | 1 | Act: the model asks for a tool call. Rakitsu runs it (several calls in the same turn run in parallel). | Act | Act -> Observe [result] |  |
| hero | 2 | Observe: the tool result is fenced, truncated if long, and appended to the history as new input. Then the loop goes back to Think (it passes through Reflect only if reflection is enabled). | Observe | Observe -> Reflect; Observe -> Think [reflection off] |  |
| hero | 3 | Reflect (optional): one extra model call without tools reviews the last action and its result. Off unless reflection is enabled. | Reflect | Reflect -> Think |  |
| hero | 4 | Think again: with the new result in the history, the model decides again. If it needs no more tools, it replies without a tool call. | Think | Think -> Answer |  |
| hero | 5 | Answer: when the model replies without a tool call, the loop ends and that reply is the result. | Answer | (none) |  |
| orch:react | 0 | Step 1/5. The supervisor calls the tool delegate_to_LogAnalyzer(task). | Supervisor, LogAnalyzer | Supervisor -> LogAnalyzer |  |
| orch:react | 1 | Step 2/5. LogAnalyzer runs its own ReAct loop and returns a result as a tool result. | Supervisor, LogAnalyzer | LogAnalyzer -> Supervisor |  |
| orch:react | 2 | Step 3/5. From that result the supervisor chooses the next worker. The order is the model's decision. | Supervisor, APISpecialist | Supervisor -> APISpecialist |  |
| orch:react | 3 | Step 4/5. The result comes back the same way. | Supervisor, APISpecialist | APISpecialist -> Supervisor |  |
| orch:react | 4 | Step 5/5. Nothing left to delegate: the supervisor writes the final answer. | Supervisor | (none) |  |
| orch:pipeline | 0 | Step 1/3. The Planner's output is passed to the Developer as context. Order is fixed in config. | Planner, Developer | Planner -> Developer |  |
| orch:pipeline | 1 | Step 2/3. The Developer answers: 'Done, I wrote the file.' The gate ignores the words and reads the tool-call record instead. | Developer, gate | Developer -> gate; tool log -> gate [record] |  |
| orch:pipeline | 2 | Step 3/3. checkRequireToolCall: the record shows 0 write_file calls. The step fails, whatever the agent claimed. | gate(bad) | tool log -> gate [record] |  |
| orch:hier | 0 | Step 1/5. The synthesized supervisor delegates to Worker A through a delegation tool. | Orchestrator, Worker A | Orchestrator -> Worker A |  |
| orch:hier | 1 | Step 2/5. Worker A returns its result. | Orchestrator, Worker A | Worker A -> Orchestrator |  |
| orch:hier | 2 | Step 3/5. Then Worker B. | Orchestrator, Worker B | Orchestrator -> Worker B |  |
| orch:hier | 3 | Step 4/5. Worker B returns its result. | Orchestrator, Worker B | Worker B -> Orchestrator |  |
| orch:hier | 4 | Step 5/5. The supervisor combines the results into one answer. | Orchestrator | (none) |  |
| orch:loop | 0 | Step 1/4. Iteration 1: the Worker's output goes to the condition_agent. | Worker agent, Judge | Worker agent -> Judge |  |
| orch:loop | 1 | Step 2/4. The Judge does not answer done, so the loop repeats. | Worker agent, Judge | Judge -> Worker agent |  |
| orch:loop | 2 | Step 3/4. Iteration 2: improved output goes to the Judge again. | Worker agent, Judge | Worker agent -> Judge |  |
| orch:loop | 3 | Step 4/4. The Judge returns done. The loop ends (or it would stop at max_iterations). | Judge, done | Judge -> done |  |
| orch:pipeline+checkbox | 0 | Step 1/4. The Planner's output is passed to the Developer as context. Order is fixed in config. | Planner, Developer | Planner -> Developer |  |
| orch:pipeline+checkbox | 1 | Step 2/4. The Developer answers: 'Done, I wrote the file.' The gate ignores the words and reads the tool-call record instead. | Developer, gate | Developer -> gate; tool log -> gate [record] |  |
| orch:pipeline+checkbox | 2 | Step 3/4. The record shows a write_file call. The step passes and the Reviewer starts. | gate, Reviewer | gate -> Reviewer; tool log -> gate [record] |  |
| orch:pipeline+checkbox | 3 | Step 4/4. The pipeline continues to its end. Each step can also be parallel or a loop. | Reviewer | (none) |  |
| wake | 0 | Checks ran and nothing changed. 0 model calls. |  | ? -> ? | ticks 1model calls 0quiet ticks cost 0 calls |
| wake | 1 | Checks ran and nothing changed. 0 model calls. |  | ? -> ? | ticks 2model calls 0quiet ticks cost 0 calls |
| wake | 2 | Checks ran and nothing changed. 0 model calls. |  | ? -> ? | ticks 3model calls 0quiet ticks cost 0 calls |
| wake | 3 | Checks ran and nothing changed. 0 model calls. |  | ? -> ? | ticks 4model calls 0quiet ticks cost 0 calls |
| wake | 4 | A check reports a change. The session starts one agent turn (L2): 1 model call. | LLM | ? -> LLM [event] | ticks 5model calls 1quiet ticks cost 0 calls |
| wake | 5 | Checks ran and nothing changed. 0 model calls. |  | ? -> ? | ticks 6model calls 1quiet ticks cost 0 calls |
| wake | 6 | Checks ran and nothing changed. 0 model calls. |  | ? -> ? | ticks 7model calls 1quiet ticks cost 0 calls |
| wake | 7 | A check raises an alarm. Another agent turn: 1 model call. | LLM | ? -> LLM [event] | ticks 8model calls 2quiet ticks cost 0 calls |
| wake | 8 | The same alarm with no state change is skipped (single_flight). 0 model calls. |  | ? -> ? | ticks 9model calls 2quiet ticks cost 0 calls |
| wake | 9 | Checks ran and nothing changed. 0 model calls. |  | ? -> ? | ticks 10model calls 2quiet ticks cost 0 calls |
| wake | 10 | Checks ran and nothing changed. 0 model calls. |  | ? -> ? | ticks 11model calls 2quiet ticks cost 0 calls |
| wake | 11 | Checks ran and nothing changed. 0 model calls. |  | (none) | ticks 12model calls 2quiet ticks cost 0 calls |
| memory | 0 | 1. The task text is split into terms (highlighted when a note contains them). |  | (none) |  |
| memory | 1 | 2. Each note is scored with BM25 over its title, content and tags. |  | (none) |  |
| memory | 2 | 3. The best top_k notes are kept. Notes with no matching term are never selected. |  | (none) |  |
| memory | 3 | 4. They are injected as a fenced block at the start of the run, into the model's working history only. The saved session does not contain it. |  | (none) |  |
| flow:node=YAML config - settings tools agents | steps 11 | (no caption) | YAML config - settings tools agents | distinct lit-edge states seen: 10 | |
| flow:node=Runtime - builds the system | steps 11 | (no caption) | Runtime - builds the system | distinct lit-edge states seen: 10 | |
| flow:node=Agent loop - ReAct | steps 11 | (no caption) | Agent loop - ReAct | distinct lit-edge states seen: 10 | |
| flow:node=LLM provider - per agent | steps 11 | (no caption) | LLM provider - per agent | distinct lit-edge states seen: 10 | |
| flow:node=Tools - cli fs mcp a2a | steps 11 | (no caption) | Tools - cli fs mcp a2a | distinct lit-edge states seen: 10 | |
| flow:node=Event bus - typed events | steps 11 | (no caption) | Event bus - typed events | distinct lit-edge states seen: 10 | |
| flow:node=SSE hub - rakitsu serve | steps 11 | (no caption) | SSE hub - rakitsu serve | distinct lit-edge states seen: 10 | |
| flow:node=Web UI - builder, debugger | steps 11 | (no caption) | Web UI - builder, debugger | distinct lit-edge states seen: 10 | |
| protocols:node=Editor - ACP client | steps 9 | (no caption) | Editor - ACP client | distinct lit-edge states seen: 8 | |
| protocols:node=MCP client - POST /mcp | steps 9 | (no caption) | MCP client - POST /mcp | distinct lit-edge states seen: 8 | |
| protocols:node=Rakitsu - agents + tools | steps 9 | (no caption) | Rakitsu - agents + tools | distinct lit-edge states seen: 8 | |
| protocols:node=MCP server - tools | steps 9 | (no caption) | MCP server - tools | distinct lit-edge states seen: 8 | |
| protocols:node=Other rakitsu - A2A agent | steps 9 | (no caption) | Other rakitsu - A2A agent | distinct lit-edge states seen: 8 | |
