# Flood relief logistics replan

Rakitsu tested build: `v0.3.2 (09c37a8e)`. Python 3 and local Ollama
with `qwen3:4b-q4_K_M` are required. Use `RAKITSU_BIN` to point to the
pinned binary if needed.

Flood logistics must keep track of demand, stock, and resources from
request to delivery. Lost visibility has contributed to duplicate or
missed deliveries in documented disasters. This synthetic training
exercise narrows that problem to a warehouse, two trucks, and three
shelters. [GAO disaster logistics findings](https://www.gao.gov/assets/gao-09-433t.pdf)

```sh
ollama pull qwen3:4b-q4_K_M
python3 run.py base
python3 run.py closure
```

Intake reconciles duplicate requests and time-stamped road reports.
The planner evaluates feasible one-stop trips and maximizes weighted
units delivered, breaking ties by travel time. This small solver
uses full loads up to each truck's capacity or the requested quantity.
The verifier reads the original facts independently and checks the
saved plan for route eligibility, duplicate stops, capacity, and stock.
All three steps must call their assigned tools. The final JSON is for
human review. This exercise never dispatches trucks or sends messages.
