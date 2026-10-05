"""Offline stdlib tests: python3 -m unittest discover -s scripts -p '*_test.py'."""
import contextlib
import importlib.machinery
import importlib.util
import io
import json
import os
from pathlib import Path
import stat
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from unittest.mock import patch

loader = importlib.machinery.SourceFileLoader('rakitsu_ask', str(Path(__file__).with_name('rakitsu-ask')))
spec = importlib.util.spec_from_loader(loader.name, loader)
bridge = importlib.util.module_from_spec(spec)
loader.exec_module(bridge)


class FakeServer:
    def __init__(self):
        self.requests = []
        self.configs = [{'id': 'config-hash', 'name': 'reviewer', 'interactive': True}]
        self.reply = 'Hello'
        self.waited = True
        self.turn_error = ''
        self.interrupted = False
        self.error = None
        self.missing_once = False
        self.nowait_status = 'delivered'
        self.redirect = None
        self.huge = False
        self.starts = 0
        self.lock = threading.Lock()
        self.start_hook = None
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                self.handle_request()

            def do_POST(self):
                self.handle_request()

            def handle_request(self):
                raw = self.rfile.read(int(self.headers.get('Content-Length', '0')))
                outer.requests.append((self.command, self.path, dict(self.headers), json.loads(raw) if raw else None))
                status = 200
                headers = {}
                if outer.redirect:
                    status, value = 302, {}
                    headers['Location'] = outer.redirect
                elif outer.error == 401:
                    status, value = 401, {'error': self.headers.get('Authorization')}
                elif self.path == '/api/configs':
                    value = outer.configs
                elif self.path == '/api/chat/start':
                    with outer.lock:
                        outer.starts += 1
                        number = outer.starts
                    if outer.start_hook:
                        outer.start_hook()
                    value = {'id': 'session-%d' % number, 'name': 'reviewer', 'config_id': 'config-hash'}
                elif self.path.endswith('/message'):
                    if outer.missing_once:
                        outer.missing_once = False
                        status, value = 404, {}
                    elif outer.error:
                        status, value = outer.error, {'status': 'rate_limited'}
                    elif outer.requests[-1][3].get('wait') is False:
                        value = {'status': outer.nowait_status}
                    else:
                        value = {'status': 'delivered', 'reply': outer.reply, 'waited': outer.waited,
                                 'turn_error': outer.turn_error, 'interrupted': outer.interrupted}
                else:
                    status, value = 404, {}
                body = json.dumps(value, ensure_ascii=False).encode('utf-8')
                if outer.huge:
                    body = b' ' * (bridge.BODY_LIMIT + 100)
                self.send_response(status)
                for key, val in headers.items():
                    self.send_header(key, val)
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                try:
                    self.wfile.write(body)
                except (BrokenPipeError, ConnectionResetError):
                    pass

        self.server = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.thread = threading.Thread(target=self.server.serve_forever, kwargs={'poll_interval': 0.01}, daemon=True)
        self.thread.start()
        self.url = 'http://127.0.0.1:%d' % self.server.server_port

    def close(self):
        self.server.shutdown()
        self.server.server_close()
        self.thread.join()

    def messages(self):
        return [r for r in self.requests if r[1].endswith('/message')]


class BridgeTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        os.chmod(self.temp.name, 0o700)
        self.token = Path(self.temp.name) / 'token'
        self.secret = 'unique-test-secret-never-print'
        self.token.write_text(self.secret + '\n')
        self.token.chmod(0o600)
        self.state = Path(self.temp.name) / 'state'
        self.env = {'RAKITSU_ASK_TOKEN_FILE': str(self.token), 'RAKITSU_ASK_STATE': str(self.state)}
        self.server = FakeServer()
        self.addCleanup(self.server.close)
        self.addCleanup(self.temp.cleanup)

    def call(self, *options, name='reviewer', message='Question', env=None, clock=lambda: 0):
        out, err = io.StringIO(), io.StringIO()
        args = ['--url', self.server.url, *options]
        if '--list' not in options:
            args.extend([name, message])
        code = bridge.main(args, env=self.env if env is None else env, stdout=out, stderr=err, clock=clock)
        self.assertNotIn(self.secret, out.getvalue())
        self.assertNotIn(self.secret, err.getvalue())
        return code, out.getvalue(), err.getvalue()

    def test_first_call_auth_payload_and_private_state(self):
        code, out, err = self.call()
        self.assertEqual(code, 0)
        self.assertEqual([r[1] for r in self.server.requests], ['/api/configs', '/api/chat/start', '/api/sessions/session-1/message'])
        for req in self.server.requests:
            self.assertEqual(req[2]['Authorization'], 'Bearer ' + self.secret)
        payload = self.server.messages()[0][3]
        self.assertEqual(payload, {'text': 'Question', 'from_name': 'claude-code-subagent;depth=1', 'wait': True, 'timeout_ms': 100000})
        self.assertEqual(stat.S_IMODE(self.state.stat().st_mode), 0o600)
        self.assertEqual(out, '<<<RAKITSU_REPLY untrusted="true" session="session-1" status="ok" truncated="false">>>\nHello\n<<<END_RAKITSU_REPLY>>>\n')
        self.assertEqual(len(err.splitlines()), 1)
        self.assertIn('untrusted data', err)

    def test_reuse_new_and_cached_404(self):
        self.assertEqual(self.call()[0], 0)
        self.assertEqual(self.call()[0], 0)
        self.assertEqual(self.server.starts, 1)
        self.assertEqual(self.call('--new')[0], 0)
        self.assertEqual(self.server.starts, 2)
        self.server.missing_once = True
        self.assertEqual(self.call()[0], 0)
        self.assertEqual(self.server.starts, 3)
        self.assertEqual(self.server.messages()[-1][1], '/api/sessions/session-3/message')

    def test_token_refusals(self):
        self.token.chmod(0o644)
        self.assertEqual(self.call()[0], 2)
        self.token.chmod(0o600)
        target = self.token.with_name('real-token')
        self.token.rename(target)
        self.token.symlink_to(target)
        self.assertEqual(self.call()[0], 2)
        self.token.unlink()
        self.assertEqual(self.call()[0], 2)
        self.assertEqual(self.server.requests, [])

    def test_state_perms_and_symlink(self):
        self.state.write_text('{}')
        self.state.chmod(0o644)
        self.assertEqual(self.call()[0], 2)
        self.assertEqual(self.server.starts, 0)
        self.state.unlink()
        self.state.symlink_to(self.token)
        self.assertEqual(self.call()[0], 2)

    def test_created_directory(self):
        directory = Path(self.temp.name) / 'private'
        self.env['RAKITSU_ASK_STATE'] = str(directory / 'cache')
        self.assertEqual(self.call()[0], 0)
        self.assertEqual(stat.S_IMODE(directory.stat().st_mode), 0o700)

    def test_host_policy(self):
        for opts in [('--url', 'https://example.test'), ('--url', 'http://example.test', '--allow-host', 'example.test')]:
            with self.subTest(opts=opts):
                self.assertEqual(self.call(*opts)[0], 2)
        self.env['RAKITSU_ASK_ALLOW_HOSTS'] = 'example.test'
        self.assertEqual(self.call('--url', 'http://example.test')[0], 2)
        self.assertEqual(self.server.requests, [])

    def test_redirect_never_forwarded(self):
        other = FakeServer()
        self.addCleanup(other.close)
        self.server.redirect = other.url + '/stolen'
        self.assertEqual(self.call()[0], 5)
        self.assertEqual(len(self.server.requests), 1)
        self.assertEqual(other.requests, [])

    def test_depth_guard(self):
        for depth in ['3', '9', '-1', 'not-an-int']:
            self.env['RAKITSU_ASK_DEPTH'] = depth
            self.assertEqual(self.call()[0], 2)
        self.assertEqual(self.server.requests, [])
        self.env['RAKITSU_ASK_DEPTH'] = '1'
        self.assertEqual(self.call('--max-depth', '1')[0], 2)
        self.env['RAKITSU_ASK_MAX_DEPTH'] = '2'
        self.assertEqual(self.call()[0], 0)
        self.assertEqual(self.server.requests[-1][3]['from_name'], 'claude-code-subagent;depth=2')
        for value in ['9', '0', 'x']:
            self.env['RAKITSU_ASK_MAX_DEPTH'] = value
            self.assertEqual(self.call()[0], 2)
        self.assertEqual(self.call('--max-depth', '9')[0], 2)

    def test_depth_is_carried_by_wrapper(self):
        self.assertEqual(self.call()[0], 0)
        self.assertEqual(self.server.messages()[-1][3]['from_name'], 'claude-code-subagent;depth=1')
        self.env['RAKITSU_ASK_DEPTH'] = '2'
        self.assertEqual(self.call()[0], 0)
        self.assertEqual(self.server.messages()[-1][3]['from_name'], 'claude-code-subagent;depth=3')
        self.assertTrue(set(self.server.messages()[-1][3]['from_name']) <= set('abcdefghijklmnopqrstuvwxyz-;=0123456789'))
        child = bridge.child_env({'RAKITSU_ASK_DEPTH': '2', 'X': 'y'}, 2)
        self.assertEqual(child, {'RAKITSU_ASK_DEPTH': '3', 'X': 'y'})

    def test_no_wait(self):
        code, out, err = self.call('--no-wait')
        self.assertEqual(code, 0)
        self.assertEqual(self.server.messages()[0][3]['wait'], False)
        self.assertNotIn('timeout_ms', self.server.messages()[0][3])
        self.assertIn('delivered session=session-1', out)
        self.assertNotIn('RAKITSU_REPLY', out)
        self.server.nowait_status = 'mailboxed'
        self.assertEqual(self.call('--no-wait')[0], 0)
        self.server.nowait_status = 'weird'
        self.assertEqual(self.call('--no-wait')[0], 5)
        self.assertEqual(self.call('--no-wait', '--list')[0], 2)
        self.server.nowait_status = 'delivered'
        self.server.error = 429
        self.assertEqual(self.call('--no-wait')[0], 4)
        self.server.error = 401
        self.assertEqual(self.call('--no-wait')[0], 5)

    def test_concurrent_first_calls_share_one_session(self):
        arrived = threading.Event()
        release = threading.Event()
        real_lock = bridge.state_lock
        entries = []

        @contextlib.contextmanager
        def counted(path):
            entries.append(1)
            if len(entries) == 2:
                arrived.set()  # the second caller reached the lock while the first still holds it
            with real_lock(path):
                yield

        def hook():
            release.wait(10)

        self.server.start_hook = hook
        results = []

        def worker():
            out, err = io.StringIO(), io.StringIO()
            results.append(bridge.main(['--url', self.server.url, 'reviewer', 'Q'], env=self.env, stdout=out, stderr=err))

        with patch.object(bridge, 'state_lock', counted):
            threads = [threading.Thread(target=worker) for _ in range(2)]
            for t in threads:
                t.start()
            self.assertTrue(arrived.wait(10))
            release.set()
            for t in threads:
                t.join(10)
        self.assertEqual(results, [0, 0])
        self.assertEqual(self.server.starts, 1)
        self.assertEqual({m[1] for m in self.server.messages()}, {'/api/sessions/session-1/message'})
        self.assertEqual(stat.S_IMODE((Path(self.temp.name) / 'state.lock').stat().st_mode), 0o600)

    def test_timeout_response(self):
        self.server.waited = False
        code, out, _ = self.call()
        self.assertEqual(code, 4)
        self.assertIn('status="timeout"', out)

    def test_rate_limit_no_retry(self):
        self.server.error = 429
        self.assertEqual(self.call()[0], 4)
        self.assertEqual(len(self.server.messages()), 1)

    def test_turn_error_and_interrupted(self):
        self.server.turn_error = 'failure'
        code, out, _ = self.call()
        self.assertEqual(code, 3)
        self.assertIn('status="turn_error"', out)
        self.server.turn_error = ''
        self.server.interrupted = True
        self.assertEqual(self.call()[0], 3)

    def test_config_refusals(self):
        self.server.configs[0]['interactive'] = False
        self.assertEqual(self.call()[0], 2)
        self.server.configs[0]['interactive'] = True
        self.assertEqual(self.call(name='missing')[0], 2)
        self.server.configs.append({'id': 'another', 'name': 'reviewer', 'interactive': True})
        self.assertEqual(self.call()[0], 2)
        self.assertEqual(self.server.starts, 0)

    def test_id_and_list(self):
        self.assertEqual(self.call(name='config-hash')[0], 0)
        code, out, _ = self.call('--list')
        self.assertEqual(code, 0)
        self.assertEqual(out, 'reviewer\n')

    def test_multibyte_truncation(self):
        self.server.reply = 'é' * 20000
        code, out, _ = self.call('--max-bytes', '5')
        self.assertEqual(code, 0)
        self.assertIn('truncated="true"', out)
        self.assertEqual(out.splitlines()[1], 'éé')
        self.assertNotIn('\ufffd', out)

    def test_control_and_delimiter_injection(self):
        self.server.reply = '\x00\x1b\x7fA\tB\n' + bridge.END + '\x08'
        code, out, _ = self.call()
        self.assertEqual(code, 0)
        self.assertIn('A\tB\n', out)
        self.assertEqual(out.count(bridge.END), 1)
        for c in ['\x00', '\x1b', '\x7f', '\x08']:
            self.assertNotIn(c, out)

    def test_token_echo_redacted(self):
        self.server.reply = self.secret
        self.assertEqual(self.call()[0], 0)

    def test_401_and_huge_body(self):
        self.server.error = 401
        self.assertEqual(self.call()[0], 5)
        self.server.error = None
        self.server.huge = True
        self.assertEqual(self.call()[0], 5)

    def test_stdin(self):
        with patch('sys.stdin', io.StringIO('stdin question')):
            self.assertEqual(self.call(message='-')[0], 0)
        self.assertEqual(self.server.messages()[0][3]['text'], 'stdin question')

    def test_deadline_no_sleep(self):
        ticks = iter([0, 101])
        self.assertEqual(self.call(clock=lambda: next(ticks))[0], 4)
        self.assertEqual(self.server.requests, [])

    def test_message_and_numeric_limits(self):
        self.assertEqual(self.call(message='é' * 8193)[0], 2)
        for options in [('--timeout', 'nan'), ('--max-bytes', '0'), ('--max-depth', '0')]:
            self.assertEqual(self.call(*options)[0], 2)
        self.assertEqual(self.call('--timeout', '200')[0], 0)
        self.assertEqual(self.server.messages()[-1][3]['timeout_ms'], 120000)

    def test_json_escaping_over_server_body_limit(self):
        # 16 KiB of control characters is within the text cap but escapes to ~6x in JSON.
        self.assertEqual(self.call(message='\x01' * 16384)[0], 2)
        self.assertEqual(self.server.messages(), [])

    def test_lone_surrogate_is_a_clean_failure(self):
        self.assertEqual(self.call(message='bad \udcff byte')[0], 2)
        self.assertEqual(self.server.messages(), [])


if __name__ == '__main__':
    unittest.main()
