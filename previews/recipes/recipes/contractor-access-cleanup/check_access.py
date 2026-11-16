"""Apply the sample's access-review policy to two local CSV exports."""
import csv
from datetime import date
from pathlib import Path
from tempfile import NamedTemporaryFile

REVIEW_DATE = date(2026, 11, 14)
REPORT = Path('output/access-review.csv')


def read_rows(filename, fields):
    with (Path('fixtures') / filename).open(encoding='utf-8', newline='') as file:
        reader = csv.DictReader(file, strict=True)
        if reader.fieldnames != list(fields):
            raise ValueError(f'{filename}: expected columns {", ".join(fields)}')
        for row in reader:
            if set(row) != set(fields) or any(not row[field] or not row[field].strip() for field in fields):
                raise ValueError(f'{filename}:{reader.line_num}: incomplete or extra fields')
            yield [row[field].strip() for field in fields]


def build_report():
    contracts = {}
    for contractor, end in read_rows('contracts.csv', ('contractor', 'contract_end')):
        if contractor in contracts:
            raise ValueError(f'duplicate contract for {contractor}')
        contracts[contractor] = date.fromisoformat(end)

    rows = []
    grants = set()
    for contractor, system, status in read_rows('access.csv', ('contractor', 'system', 'status')):
        grant = (contractor, system)
        if contractor not in contracts or grant in grants or status not in {'active', 'disabled'}:
            raise ValueError(f'invalid, duplicate, or unmatched grant: {contractor} / {system}')
        grants.add(grant)
        end = contracts[contractor]
        if status == 'disabled':
            decision, reason = 'NO ACTION', 'Grant already disabled'
        elif end < REVIEW_DATE:
            decision, reason = 'REVIEW', 'Active grant after contract end'
        else:
            decision, reason = 'NO ACTION', 'Contract has not ended before the review date'
        rows.append((*grant, end.isoformat(), status, decision, reason))
    return rows


def main():
    try:
        rows = build_report()
        REPORT.parent.mkdir(exist_ok=True)
        with NamedTemporaryFile(dir=REPORT.parent, mode='w', encoding='utf-8', newline='', delete=False) as file:
            temporary = Path(file.name)
            try:
                writer = csv.writer(file)
                writer.writerow(('contractor', 'system', 'contract_end', 'status', 'decision', 'reason'))
                writer.writerows(rows)
                file.close()
                temporary.replace(REPORT)
            finally:
                temporary.unlink(missing_ok=True)
    except (ValueError, OSError, csv.Error) as error:
        raise SystemExit(f'Access check failed: {error}') from error
    print(f'Wrote {REPORT}')


if __name__ == '__main__':
    main()
