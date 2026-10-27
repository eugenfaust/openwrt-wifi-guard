#!/usr/bin/env python3
"""Exercise the compiled CLI with a subprocess fake; never contact a router."""
import json
import os
from pathlib import Path
import signal
import subprocess
import sys
import tempfile
import time

BINARY = str(Path(sys.argv[1]).resolve())
FAKE = r'''#!/usr/bin/env python3
import json, os, sys, time
from pathlib import Path
root = Path(os.environ['WIFI_GUARD_FIXTURE'])
args = sys.argv[1:]
if args[0] == 'list':
    print('hostapd.ap0\nhostapd.ap1')
elif args[0] == 'call':
    clients = {'02:00:00:00:00:01': {'authorized': True}} if args[1] == 'hostapd.ap0' else {}
    if (root / 'new-connected').exists() and args[1] == 'hostapd.ap1':
        clients['02:00:00:00:00:02'] = {'authorized': True}
    print(json.dumps({'clients': clients}))
elif args[0] == 'subscribe':
    with (root / 'subscriptions').open('a') as f:
        f.write('started\n')
    time.sleep(.15)
    print('{invalid', flush=True)
    for kind in ('probe', 'auth', 'assoc'):
        print(json.dumps({kind: {'address': '02:00:00:00:00:09', 'ifname': 'ap0'}}), flush=True)
    print(json.dumps({'sta-authorized': {'address': '02:00:00:00:00:01', 'ifname': 'ap0'}}), flush=True)
    (root / 'new-connected').touch()
    for _ in range(3):
        print(json.dumps({'sta-authorized': {'address': '02:00:00:00:00:02', 'ifname': 'ap1'}}), flush=True)
    time.sleep(.15)
    # Exit to force ubus restart/resubscribe.
'''


def wait_until(predicate, timeout=5):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(.02)
    raise AssertionError('smoke timeout')


def main():
    # Honor TMPDIR; CI uses its standard temp area, Hermes supplies its scratch dir.
    with tempfile.TemporaryDirectory(prefix='wifi-guard-smoke-') as tmp:
        root = Path(tmp)
        fake = root / 'ubus'
        fake.write_text(FAKE)
        fake.chmod(0o700)
        state = root / 'state.json'
        env = dict(os.environ, WIFI_GUARD_FIXTURE=tmp)
        args = [BINARY, '-ubus', str(fake), '-state', str(state), '-socket', str(root / 'control.sock'), '-uci-dir', str(root),
                '-reconcile', '80ms', '-retry', '40ms', '-flush', '40ms']

        def start(log):
            return subprocess.Popen(args, env=env, stdout=subprocess.DEVNULL, stderr=log)

        def stop(proc):
            proc.send_signal(signal.SIGTERM)
            try:
                assert proc.wait(timeout=3) == 0, 'daemon failed on SIGTERM'
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait()
                raise

        logpath = root / 'first.log'
        with logpath.open('w') as log:
            proc = start(log)
            try:
                wait_until(lambda: state.exists())
                # A different/disabled RPC socket must not bypass registry ownership.
                competing_args = args.copy()
                competing_args[competing_args.index('-socket') + 1] = ''
                competing = subprocess.run(competing_args, env=env, capture_output=True,
                                           text=True, timeout=3)
                assert competing.returncode != 0, 'concurrent registry writer accepted'
                assert 'registry is already in use' in competing.stderr, competing.stderr
                assert proc.poll() is None, 'owner exited during contention test'
                live_dump = subprocess.check_output([BINARY, '-state', str(state), '-dump'], text=True)
                assert json.loads(live_dump)['initialized'], 'readonly dump blocked by writer lock'
                wait_until(lambda: (root / 'subscriptions').read_text().count('started') >= 2)
                wait_until(lambda: len(json.loads(state.read_text())['devices']) == 2)
                def rpc_call(method, params=None):
                    return json.loads(subprocess.check_output(
                        [BINARY, 'rpc', '-socket', str(root / 'control.sock'), 'call', method],
                        input=json.dumps(params or {}), text=True))
                public = rpc_call('settings')
                assert public == {'enabled': False, 'chat_id': '', 'token_set': False}
                assert len(rpc_call('devices')['devices']) == 2
                assert rpc_call('rename', {'mac': '02:00:00:00:00:01', 'name': 'Test phone'})['ok']
                assert rpc_call('trust', {'mac': '02:00:00:00:00:01', 'trust': 'trusted'})['ok']
                updated = next(d for d in rpc_call('devices')['devices'] if d['mac'] == '02:00:00:00:00:01')
                assert updated['friendly_name'] == 'Test phone' and updated['trust'] == 'trusted'
                denied = subprocess.run([BINARY, 'rpc', '-socket', str(root / 'control.sock'), 'call', 'trust'],
                                        input=json.dumps({'mac': '02:00:00:00:00:01', 'trust': 'blocked'}),
                                        capture_output=True, text=True)
                assert denied.returncode != 0, 'reserved blocking exposed'
            finally:
                stop(proc)
        events = [json.loads(line) for line in logpath.read_text().splitlines()]
        alerts = [e for e in events if e['msg'] == 'new_device']
        assert len(alerts) == 1, alerts
        assert alerts[0]['mac'] == '02:00:00:00:00:02', alerts
        snapshot = json.loads(state.read_text())
        assert snapshot['initialized'] and len(snapshot['devices']) == 2
        assert snapshot['devices']['02:00:00:00:00:01']['trust'] == 'trusted'
        assert snapshot['devices']['02:00:00:00:00:02']['trust'] == 'unknown'
        assert (state.stat().st_mode & 0o777) == 0o600

        second = root / 'second.log'
        with second.open('w') as log:
            proc = start(log)
            try:
                wait_until(lambda: (root / 'subscriptions').read_text().count('started') >= 3)
                time.sleep(.4)
            finally:
                stop(proc)
        assert not any(json.loads(line)['msg'] == 'new_device'
                       for line in second.read_text().splitlines()), 'restart spam'
        dump = subprocess.check_output([BINARY, '-state', str(state), '-dump'], text=True)
        assert len(json.loads(dump)['devices']) == 2
        # A corrupt registry must not be silently discarded (and cause re-alerts).
        state.write_text('{broken')
        corrupt = subprocess.run([BINARY, '-state', str(state), '-dump'],
                                 capture_output=True, text=True)
        assert corrupt.returncode != 0
        print('PASS: silent bootstrap, ignore probe/auth/assoc, one new-device log, '
              'dedup, ubus resubscribe, restart persistence, SIGTERM, private RPC/actions/settings, dump, corrupt-state refusal')


if __name__ == '__main__':
    main()
