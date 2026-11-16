import argparse
import json
from pathlib import Path
from driver import BACKENDS, decide

parser = argparse.ArgumentParser(description="Ask a runtime yes/no Decision question.")
parser.add_argument("--backend", choices=BACKENDS, default="laya")
parser.add_argument("--question", required=True)
parser.add_argument("--input", required=True)
args = parser.parse_args()
if not args.question.strip() or not args.input.strip():
    parser.error("Question and input must be nonempty.")
config = json.loads(Path(__file__).with_name("config.json").read_text())
decision = config["orchestrator"]["pipeline"]["steps"][0]["decision"]
decision["backend"] = args.backend
decision["questions"]["result"]["instructions"] = args.question
print(json.dumps({"backend": args.backend, "answer": decide(config, args.input)}))
