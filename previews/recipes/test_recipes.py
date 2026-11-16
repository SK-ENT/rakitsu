from contextlib import closing
from functools import partial
from http.client import HTTPConnection
from http.server import ThreadingHTTPServer
import json
from pathlib import Path
import subprocess
import sys
from tempfile import TemporaryDirectory
from threading import Thread
import unittest

from serve import CatalogHandler

FLOOD = Path(__file__).resolve().parent / 'recipes' / 'flood-relief-replan'


class RecipeTests(unittest.TestCase):
    def test_analytics_requires_same_origin_fetch_metadata(self):
        with TemporaryDirectory() as directory:
            site = Path(directory)
            (site / 'catalog.json').write_text('[{"id":"test","title":"Test recipe"}]')
            handler = partial(CatalogHandler, directory=site)
            with ThreadingHTTPServer(('127.0.0.1', 0), handler) as server:
                server.database = site / 'counts.sqlite3'
                thread = Thread(target=server.serve_forever, daemon=True)
                thread.start()
                try:
                    payload = json.dumps({'recipe': 'test', 'event': 'view'})
                    for fetch_site, status in [(None, 403), ('cross-site', 403), ('same-origin', 204)]:
                        with self.subTest(fetch_site=fetch_site):
                            headers = {'Content-Type': 'application/json'}
                            if fetch_site is not None:
                                headers['Sec-Fetch-Site'] = fetch_site
                            with closing(HTTPConnection(*server.server_address, timeout=5)) as connection:
                                connection.request('POST', '/analytics/event', payload, headers)
                                self.assertEqual(connection.getresponse().status, status)
                finally:
                    server.shutdown()
                    thread.join()

    def test_flood_verifier_requires_full_integer_load(self):
        with TemporaryDirectory() as directory:
            scenario = Path(directory)
            (scenario / 'scenario.json').write_bytes((FLOOD / 'fixtures' / 'base.json').read_bytes())

            def run(stage):
                return subprocess.run([sys.executable, str(FLOOD / 'dispatch.py'), stage],
                                      cwd=scenario, capture_output=True, text=True)

            for stage in ('intake', 'plan', 'verify'):
                result = run(stage)
                self.assertEqual(result.returncode, 0, result.stderr)
            path = scenario / 'output' / 'plan.json'
            trips = json.loads(path.read_text())
            for units in (1, True, float(trips[0]['units']), 0):
                with self.subTest(units=units):
                    path.write_text(json.dumps([{**trips[0], 'units': units}, *trips[1:]]))
                    result = run('verify')
                    self.assertNotEqual(result.returncode, 0)
                    self.assertIn('Trip amount must equal the planner allocation.', result.stderr)

            path.write_text(json.dumps(trips))
            fixture = scenario / 'scenario.json'
            data = json.loads(fixture.read_text())
            data['reports'] = [row for row in data['reports'] if row['site'] != trips[0]['site']]
            fixture.write_text(json.dumps(data))
            result = run('verify')
            self.assertNotEqual(result.returncode, 0)
            self.assertIn('Missing reconciled demand for the trip site.', result.stderr)


if __name__ == '__main__':
    unittest.main()
