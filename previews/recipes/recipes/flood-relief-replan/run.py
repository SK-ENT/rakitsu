"""Run a clean scenario and print its verified review packet."""
import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
from tempfile import TemporaryDirectory

root = Path(__file__).resolve().parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument('scenario', choices=['base', 'closure'])
args = parser.parse_args()
binary = os.environ.get('RAKITSU_BIN', 'rakitsu')
pin = 'rakitsu version v0.3.2 (09c37a8e)'
if subprocess.check_output([binary, 'version'], text=True).splitlines()[0] != pin:
    raise SystemExit('Use Rakitsu v0.3.2 (09c37a8e).')
scenario = json.loads((root / 'fixtures/base.json').read_text())
if args.scenario == 'closure':
    scenario['roads'] += json.loads((root / 'fixtures/bridge-closed.json').read_text())
with TemporaryDirectory() as work:
    directory = Path(work)
    for name in ('config.yaml', 'dispatch.py'):
        shutil.copyfile(root / name, directory / name)
    (directory / 'scenario.json').write_text(json.dumps(scenario))
    result = subprocess.run([binary, 'run', '--no-hub', '--timeout', '240', '--sessions-dir', work,
                             'config.yaml', (root / 'input.txt').read_text().strip()],
                            cwd=work, text=True, capture_output=True)
    if result.returncode:
        raise SystemExit(result.stderr.strip() or 'Rakitsu pipeline failed.')
    report = json.loads((directory / 'output/review.json').read_text())
    print(json.dumps(report, indent=2))
