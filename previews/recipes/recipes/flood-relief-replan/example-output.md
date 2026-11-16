# Flood relief logistics replan

Both fictional scenarios completed on Rakitsu `v0.3.2 (09c37a8e)` with local
Ollama `qwen3:4b-q4_K_M`. Each completed the Intake, Planner and Verifier
steps, which required the assigned local tool.

| Decision | Before the closure | After the closure |
| --- | --- | --- |
| Heavy truck | 70 units to North School via North Bridge | 60 units to East Hall via East Road |
| Light truck | 45 units to East Hall via East Road | 45 units to North School via Ridge Lane |
| Supplies delivered | 115 units | 105 units |
| Unmet requests | 15 East Hall, 30 Hill Library | 25 North School, 30 Hill Library |
| Stock left | 0 units | 10 units |

Intake counted North School's duplicate request once and kept the 09:00
closure over the stale 08:50 reopening report. The light truck could take
Ridge Lane. The heavy truck could not. A separate negative check replaced
Ridge Lane with the closed bridge and the verifier rejected the altered plan.
The result is a dispatcher review packet for a synthetic exercise.

## Captured JSON

### Before

```json
{
  "status": "VERIFIED_FOR_REVIEW",
  "trips": [
    {
      "truck": "heavy",
      "site": "North School",
      "road": "North Bridge",
      "minutes": 25,
      "units": 70
    },
    {
      "truck": "light",
      "site": "East Hall",
      "road": "East Road",
      "minutes": 35,
      "units": 45
    }
  ],
  "unmet": {
    "East Hall": 15,
    "Hill Library": 30
  },
  "remaining_stock": 0,
  "duplicate_reports": 1
}
```

### After

```json
{
  "status": "VERIFIED_FOR_REVIEW",
  "trips": [
    {
      "truck": "heavy",
      "site": "East Hall",
      "road": "East Road",
      "minutes": 35,
      "units": 60
    },
    {
      "truck": "light",
      "site": "North School",
      "road": "Ridge Lane",
      "minutes": 55,
      "units": 45
    }
  ],
  "unmet": {
    "North School": 25,
    "Hill Library": 30
  },
  "remaining_stock": 10,
  "duplicate_reports": 1
}
```
