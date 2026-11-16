"""Plan a small fictional relief load and verify the persisted plan."""
from itertools import product
import json
from pathlib import Path
import sys

data = json.loads(Path('scenario.json').read_text())
out = Path('output')
out.mkdir(exist_ok=True)


def save(name, value):
    (out / name).write_text(json.dumps(value, indent=2) + '\n')
    print(json.dumps(value))


def load(name):
    return json.loads((out / name).read_text())


def reconcile():
    needs = {}
    for record in data['reports']:
        site = record['site']
        previous = needs.get(site)
        if previous and previous['time'] == record['time'] and previous != record:
            raise ValueError(f'Conflicting demand for {site}; confirm with an operator.')
        if previous is None or record['time'] > previous['time']:
            needs[site] = record
    latest = {}
    for road in data['roads']:
        name = road['road']
        # On a tie a closure wins. A late-arriving stale report cannot reopen a road.
        if name not in latest or (road['time'], not road['open']) > (latest[name]['time'], not latest[name]['open']):
            latest[name] = road
    return {'needs': needs, 'roads': {name: item['open'] for name, item in latest.items()},
            'duplicate_reports': len(data['reports']) - len(needs)}


def candidates(intake):
    for truck, capacity in data['trucks'].items():
        yield [None] + [
            {'truck': truck, 'site': route['site'], 'road': route['road'],
             'minutes': route['minutes'], 'units': min(capacity, intake['needs'][route['site']]['units'])}
            for route in data['routes']
            if intake['roads'].get(route['road']) is True
            and truck in route['trucks'] and route['minutes'] <= data['max_trip_minutes']
        ]


def plan(intake):
    best = (-1, 0)
    selected = []
    for combo in product(*candidates(intake)):
        trips = [trip for trip in combo if trip]
        if len({t['site'] for t in trips}) != len(trips) or sum(t['units'] for t in trips) > data['stock']:
            continue
        score = (sum(t['units'] * intake['needs'][t['site']]['priority'] for t in trips),
                 -sum(t['minutes'] for t in trips))
        if score > best:
            best, selected = score, trips
    return selected


def verify(trips):
    intake = reconcile()
    trucks = set()
    delivered = {}
    for trip in trips:
        truck, site, road, units = (trip[k] for k in ('truck', 'site', 'road', 'units'))
        if truck not in data['trucks'] or truck in trucks or site in delivered:
            raise ValueError('Unknown truck or duplicate trip.')
        demand = intake['needs'].get(site)
        if demand is None:
            raise ValueError('Missing reconciled demand for the trip site.')
        matching = [r for r in data['routes'] if r['site'] == site and r['road'] == road
                    and truck in r['trucks'] and r['minutes'] == trip['minutes']]
        if not matching or intake['roads'].get(road) is not True or trip['minutes'] > data['max_trip_minutes']:
            raise ValueError('Invalid route, closure or trip window.')
        expected = min(data['trucks'][truck], demand['units'])
        if type(units) is not int or units <= 0 or units != expected:
            raise ValueError('Trip amount must equal the planner allocation.')
        trucks.add(truck)
        delivered[site] = units
    remaining = data['stock'] - sum(delivered.values())
    if remaining < 0:
        raise ValueError('Warehouse stock exceeded.')
    unmet = {site: row['units'] - delivered.get(site, 0) for site, row in intake['needs'].items()
             if row['units'] > delivered.get(site, 0)}
    return {'status': 'VERIFIED_FOR_REVIEW', 'trips': trips, 'unmet': unmet,
            'remaining_stock': remaining, 'duplicate_reports': intake['duplicate_reports']}


stage = sys.argv[1]
if stage == 'intake':
    save('intake.json', reconcile())
elif stage == 'plan':
    save('plan.json', plan(load('intake.json')))
elif stage == 'verify':
    save('review.json', verify(load('plan.json')))
else:
    raise SystemExit('Unknown stage')
