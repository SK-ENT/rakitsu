"""Map a Rakitsu Jev decision to a fixed engineering checklist."""
import json
import sys

CHECKLISTS = {
    'security': 'Check session enforcement and cross-account access.',
    'database': 'Check migration rollback and data integrity.',
    'general': 'Check behavior and run the relevant tests.',
}


def checklist(decision):
    if decision['backend'] != 'jev':
        raise ValueError('Expected a Jev decision.')
    return CHECKLISTS[decision['answers']['queue']['choice']]


if __name__ == '__main__':
    decision = next((json.loads(line) for line in sys.stdin if line.startswith('{"backend"')), None)
    if decision is None:
        raise SystemExit('Rakitsu returned no decision. No checklist was selected.')
    print(checklist(decision))
