# Long-Running Monitor: BTC Price Watcher

This example demonstrates a long-running chat session that wakes on a timer to monitor Bitcoin prices. The session runs free checks every minute; the model is called only when the price moves significantly or when configured thresholds are breached. No API key is needed for CoinGecko's simple price endpoint (see [Data source: CoinGecko](#data-source-coingecko) for limits and terms).

**Powered by CoinGecko** - price data comes from the [CoinGecko API](https://www.coingecko.com/en/api).

## What it does

1. **Free checks** run every 60 seconds (no model call, no cost)
   - Query CoinGecko API for current BTC-USD price
   - Check for price alarms (e.g., below $55,000)
   - Check for trend changes (bullish/bearish label flip)

2. **On change or alarm**, the timer injects a turn into the session
   - Model receives check results in a [TIMER-SOURCED TURN] message
   - Model explains what changed in three short lines
   - Model cannot clear or lower alarms

3. **Throttling** keeps costs low
   - Max 6 model turns per hour (hourly cap)
   - Idle backoff: intervals extend from 60s up to 10 min if quiet
   - Single-flight debounce: consecutive alarms without state change are skipped

4. **Session survives restarts**
   - Per-session audit log tracks all ticks and escalations
   - Cap window (6/hour) persists across crashes
   - Resume with `resume_id` picks up where it left off

## Variant: start a task on a bearish flip

[btc-watcher-trigger.yaml](btc-watcher-trigger.yaml) is the same watcher plus one allowlisted task. When the `btc-label` check flips to "down", the woken session may call `start_task` with `config_name: bearish-report` and `arguments: {"trend": "down"}`. That launches [tasks/bearish-report.yaml](tasks/bearish-report.yaml) as a separate run:

- Only `bearish-report` can be started, and the only value the model can pass is `trend: down`. Anything else is refused and logged.
- At most 2 starts per rolling hour and 1 at a time. The hourly count lives in the wake state file, so a restart does not reset it.
- The task runs on its own config defaults, not the watcher's environment, and cannot start further tasks.
- The full report goes to `~/.rakitsu/wake/<session-id>.tasks/<task-id>.log`. The watcher only sees a status and a short summary.
- `POST /api/chat/{session-id}/wake/stop` blocks new starts and cancels a running task. Cancel one task with `POST /api/chat/{session-id}/wake/tasks/{task-id}/cancel`.

The task path is relative to the chat workdir, so choose this example directory as the workdir when you start the chat. (The task config also shows up as a selectable config in the UI; you do not need to run it by hand.)

```bash
LITELLM_API_KEY=$(grep '^LITELLM_API_KEY=' ~/.secrets/litellm | cut -d= -f2-) \
  rakitsu serve --config-dir examples/single/14-long-running-monitor
```

## Data source: CoinGecko

**Powered by CoinGecko.** CoinGecko's API Terms of Service (section 5.3) ask users of the API to display this attribution. The watcher is told to end every alert or digest it writes with that line. If you show or republish the output anywhere, keep the attribution visible.

Things to know (a factual summary, not legal advice; read the [current terms](https://www.coingecko.com/en/api_terms) yourself, they can change without notice):

- **Rate limits.** The example calls the keyless public endpoint. Keyless limits are per IP and shared with everyone on that IP, so you may see HTTP 429. CoinGecko recommends a free Demo API key (sent as the `x-cg-demo-api-key` header). The wake `http_json` check cannot send custom headers yet, so this example does not wire a key in. If you hit 429, raise `interval_seconds` and `min_interval_seconds`.
- **No redistribution.** Do not sell, sub-license or re-distribute access to the API or its data through this example.
- **Caching.** CoinGecko does not encourage caching or storing its data. The example keeps results in the wake audit log and rolling summary on your machine. If you keep or cache the data, refresh it at least every 24 hours and delete it when you stop using the API.

## Not financial advice

This example is for demonstration only. Price monitoring is not trading advice, and the model's commentary carries no guarantee. Use at your own risk.

## Loading secrets

CoinGecko's simple/price endpoint requires no authentication. If you use LiteLLM or another provider that requires credentials, load them following the pattern from [13-voice-orchestrator](../13-voice-orchestrator/README.md):

```bash
# Create a secret file (never commit; 600 mode only you can read)
echo "LITELLM_API_KEY=your-key-here" > ~/.secrets/litellm
chmod 600 ~/.secrets/litellm

# Load it inline to a single command, never via export
LITELLM_API_KEY=$(grep '^LITELLM_API_KEY=' ~/.secrets/litellm | cut -d= -f2-) \
  rakitsu serve --config-dir examples/single/14-long-running-monitor
```

The key is scoped to the `rakitsu` process only; it never leaks into your shell or history.

## Running the example

Start the server:

```bash
LITELLM_API_KEY=$(grep '^LITELLM_API_KEY=' ~/.secrets/litellm | cut -d= -f2-) \
  rakitsu serve --config-dir examples/single/14-long-running-monitor
```

Open http://localhost:9100 in your browser. Click "New Chat" and select the config. The session starts; check results appear in the event stream every 60 seconds. When the price moves significantly, a model turn is triggered.

### Manual resume

After a crash, resume the same session:

```bash
curl -X POST http://localhost:9100/api/chat/sessions/{session-id}/wake/resume
```

This removes the kill-switch file (if present) and restarts the timer. The cap window (6/hour) and tick state persist.

### How to cancel

Stop the timer without ending the session. Choose one of these methods:

**Method 1: REST API (recommended)** — stop the timer immediately:

```bash
curl -X POST http://localhost:9100/api/chat/{session-id}/wake/stop
```

Response:
```json
{"status": "stopped"}
```

**Method 2: Manual kill-switch file** — stop at the next tick:

```bash
touch ~/.rakitsu/wake/STOP
```

The next tick detects the file and stops the loop; the session stays alive in the UI.

### Resuming after stop

The session stays open. You can still send messages, or resume the timer:

```bash
# If you used the kill-switch file, remove it first
rm ~/.rakitsu/wake/STOP

# Resume the timer
curl -X POST http://localhost:9100/api/chat/{session-id}/wake/resume
```

Response:
```json
{"status": "resumed"}
```

The timer restarts with the same configuration. The hourly cap window and tick state persist across stops and resumes.

## Interpreting the audit log

Each session writes an audit log at `~/.rakitsu/wake/{session-id}.audit.jsonl`. One JSONL line per tick:

```bash
jq '.summary_chars' ~/.rakitsu/wake/{session-id}.audit.jsonl
```

Shows the rolling summary character count at each tick. Use this to monitor context growth and verify the session is running.

## Tier and interval rules

- **L0 (free tick)**: runs checks, no model call, outcome = quiet | changed | alarm | unknown
- **L1 (judge, config-only v1)**: advisory layer; not yet implemented
- **L2 (escalation)**: model turn triggered by alarm or change; subject to hourly cap

**Interval backoff:**
- Quiet tick → interval × 1.5 (capped at 10 min)
- Any change/alarm → reset to 60s base
- Jitter ±10% per tick prevents thundering herd

**Hourly cap:**
- Max 6 turns per hour
- 7th+ turn suppressed with status `suppressed: hourly_cap`
- Cap window persists across restarts

## Verification

After 10-20 ticks, tail the audit log:

```bash
tail -20 ~/.rakitsu/wake/{session-id}.audit.jsonl | jq .
```

You should see:
- Outcome progression (quiet → changed if price moved)
- Tick numbers incrementing
- Summary character counts growing
- Escalation attempts logged when cap/suppression apply
