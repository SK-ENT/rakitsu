"""Fixed, local tools for the fictional order desk."""
from datetime import date
import json
from pathlib import Path
import sys

order = json.loads(Path('fixtures/order.json').read_text())
company = json.loads(Path('fixtures/company.json').read_text())


def options():
    shortage = max(0, order['quantity'] - company['stock'])
    if shortage == 0:
        return [{'name': 'In stock', 'on_time': True, 'shipping_cents': 0}]
    return [
        {'name': supplier['name'], 'on_time': date.fromisoformat(supplier['arrival']) <= date.fromisoformat(order['delivery_by']),
         'shipping_cents': supplier['shipping_cents']}
        for supplier in company['suppliers'] if supplier['quantity'] >= shortage
    ]


def audit():
    discount = min(order['discount_percent'], company['max_discount_percent'])
    subtotal = order['quantity'] * company['unit_price_cents'] * (100 - discount) // 100
    quotes = [
        {**option, 'quote_cents': subtotal + option['shipping_cents']}
        for option in options()
    ]
    eligible = [quote for quote in quotes if quote['on_time'] and quote['quote_cents'] <= order['budget_cents']]
    candidate = min(eligible, key=lambda quote: quote['quote_cents'], default=None)
    blockers = []
    if not eligible:
        blockers.append('No supply option meets both the requested date and budget.')
    if order['discount_percent'] > company['max_discount_percent']:
        blockers.append('Requested discount exceeds policy.')
    if not company['approved'] and (candidate is None or candidate['quote_cents'] >= company['approval_threshold_cents']):
        blockers.append('Approval is missing for a quote at or above the threshold.')
    return {
        'id': order['id'], 'decision': 'HOLD' if blockers else 'READY',
        'shortage': max(0, order['quantity'] - company['stock']),
        'discount_percent': discount, 'quotes': quotes, 'blockers': blockers,
        'selected_quote': candidate,
    }


stage = sys.argv[1]
if stage == 'sales':
    print(json.dumps(order))
elif stage == 'operations':
    print(json.dumps({'stock': company['stock'], 'options': options()}))
elif stage == 'finance':
    report = audit()
    Path('output').mkdir(exist_ok=True)
    Path('output/decision.json').write_text(json.dumps(report, indent=2) + '\n')
    print(json.dumps(report))
else:
    raise SystemExit('Unknown order desk stage.')
