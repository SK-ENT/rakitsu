"""Produce a recovery review packet. This program has no equipment integration."""
import argparse
from contextlib import nullcontext
import json
from pathlib import Path
from driver import decide
from fixture import cloud_fixtures

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--live-cloud", action="store_true", help="Call the three configured cloud APIs.")
args = parser.parse_args()
root = Path(__file__).parent
event = json.loads((root / "cell-event.json").read_text())
stages = json.loads((root / "stages.json").read_text())
# Machine facts override all model answers. Even a clear interlock only
# allows an operator review packet, never an automatic restart.
hold = event.get("safety_interlock") != "closed" or event.get("lockout_verified") is not True
answers = {}
with (nullcontext() if args.live_cloud else cloud_fixtures(stages, event)):
    for stage in stages:
        config = json.loads((root / stage["config"]).read_text())
        answers[stage["name"]] = decide(config, event[stage["input"]])
print(json.dumps({
    "cell": event["cell"],
    "disposition": "HOLD" if hold else "MANUAL_REVIEW",
    "reason": "Safety interlock or lockout is unresolved." if hold else "Operator review required.",
    "cloud_responses": "live" if args.live_cloud else "simulated",
    "decisions": answers,
}, indent=2))
