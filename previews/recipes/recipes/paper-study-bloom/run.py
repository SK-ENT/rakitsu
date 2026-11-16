"""Run the study and save the candidate for review without executing it."""
import ast
import json
import os
from pathlib import Path
import re
import subprocess
from tempfile import TemporaryDirectory
from textwrap import dedent, indent

ROOT = Path(__file__).resolve().parent
PIN = 'v0.3.2 (09c37a8e)'
STEPS = ('paper_notes', 'review', 'implementation')

binary = os.environ.get('RAKITSU_BIN', 'rakitsu')
version = subprocess.check_output([binary, 'version'], text=True).splitlines()[0]
if version != f'rakitsu version {PIN}':
    raise SystemExit(f'Use Rakitsu {PIN}.')
prompt = (ROOT / 'paper-notes.md').read_text() + '\n' + (ROOT / 'input.txt').read_text() + '\nStarter code:\n' + (ROOT / 'starter.py').read_text()
with TemporaryDirectory() as sessions:
    result = subprocess.run([binary, 'run', '--no-hub', '--timeout', '180', '--sessions-dir', sessions,
                    str(ROOT / 'config.yaml'), prompt], cwd=ROOT, capture_output=True, text=True)
    if result.returncode:
        raise SystemExit(result.stderr.strip() or 'Rakitsu study failed.')
    checkpoints = list(Path(sessions).glob('*.checkpoint.json'))
    if len(checkpoints) != 1:
        raise SystemExit('Expected one study checkpoint.')
    results = json.loads(checkpoints[0].read_text())['results']
    if any(not results.get(step, {}).get('output', '').strip() for step in STEPS):
        raise SystemExit('The study did not complete all three contributions.')
    discussion = [(step, results[step]['output'].strip()) for step in STEPS[:2]]
    candidate = results['implementation']['output'].strip()
    blocks = re.findall(r'```(?:python|py)[ \t]*\n(.*?)```', candidate, re.S)
    if len(blocks) > 1 or (not blocks and '```' in candidate):
        raise SystemExit('Expected one Python module.')
    methods = dedent(blocks[0] if blocks else candidate).strip()
    parsed = ast.parse(methods)
    if ([node.name for node in parsed.body if isinstance(node, ast.FunctionDef)] !=
            ['might_contain', 'confirm'] or len(parsed.body) != 2):
        raise SystemExit('Expected only the two requested methods.')
    code = (ROOT / 'starter.py').read_text().rstrip() + '\n\n' + indent(methods, '    ') + '\n'
    compile(code, 'bloom_precheck.py', 'exec')
    output = ROOT / 'output'
    output.mkdir(exist_ok=True)
    (output / 'bloom_precheck.py').write_text(code)
    (output / 'discussion.md').write_text(''.join(f'## {step}\n\n{text}\n\n' for step, text in discussion))
    print('Saved output/discussion.md and output/bloom_precheck.py. Review the module before testing.')
