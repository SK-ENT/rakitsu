import json
import re
import sys
from pathlib import Path

arm = sys.argv[1]
assert arm in ('baseline', 'recall', 'repair')
log = Path(f'harness/{arm}.log').read_text(errors='replace')
session_id = re.search(r'Session ID: ([\w-]+)', log).group(1)
session = Path(f'harness/state/{arm}/{session_id}.jsonl')
events = [json.loads(line) for line in session.read_text().splitlines()]
final = next(event.get('payload', {})['final_answer'] for event in reversed(events)
             if event.get('event_type') == 'EXECUTION_COMPLETE')
if arm == 'repair':
    snippet = re.fullmatch(r'```javascript\s+(.*?)\s*```', final.strip(), re.S)
    if not snippet:
        raise SystemExit('repair: model response contains no single fenced JavaScript snippet')
    code = snippet.group(1)
    if '</script' in code.lower():
        raise SystemExit('repair: snippet contains a script closing tag')
    original = Path('generated/recall/index.html').read_text()
    prelude, tag, suffix = original.rpartition('</script>')
    assert tag
    output = Path('generated/repaired/index.html')
    output.parent.mkdir(parents=True, exist_ok=True)
    output.write_text(prelude + '\n' + code + '\n' + tag + suffix)
else:
    html = re.search(r'<!doctype html\b.*?</html\s*>', final, re.I | re.S)
    if not html:
        raise SystemExit(f'{arm}: model response contains no complete HTML document')
    Path(f'generated/{arm}/index.html').write_text(html.group() + '\n')
print(f'{arm}: {session_id}')
