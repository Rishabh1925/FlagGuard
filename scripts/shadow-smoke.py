#!/usr/bin/env python3
"""Verify shadow decisions on the local demo, then restore the normal controller.

Requires the running Compose demo. Temporarily stops its enforcing controller;
uses an isolated, disposable state file and loopback port 8086 for shadow mode.
"""
import json
from pathlib import Path
import subprocess
import time
import urllib.error
import uuid

import smoke


def main():
    root = Path(__file__).resolve().parent.parent
    compose = ['docker', 'compose']
    if subprocess.run(compose + ['version'], capture_output=True).returncode:
        compose = ['docker-compose']

    def run(*args):
        subprocess.run(compose + list(args), cwd=root, check=True)

    def call(path, body=None):
        return smoke.call(path, body, base='http://localhost:8086')

    def wait(want, seconds=240):
        deadline = time.monotonic() + seconds
        while time.monotonic() < deadline:
            try:
                state = call('/api/status')['guardrails'][0]
                if state['status'] == want:
                    return state
                if state['status'] == 'ACTION_REQUIRED':
                    raise RuntimeError(state['message'])
            except OSError:
                pass
            time.sleep(3)
        raise RuntimeError('Shadow controller did not reach ' + want)

    smoke.wait_for('HEALTHY')
    controller = subprocess.check_output(compose + ['ps', '-q', 'flagguard'], cwd=root, text=True).strip()
    if not controller:
        raise RuntimeError('The normal demo controller must be running')
    name = 'flagguard-shadow-check-' + uuid.uuid4().hex[:10]
    started = False
    clean = False
    result = None
    run('stop', 'flagguard')
    try:
        run('run', '-d', '--no-deps', '--name', name,
            '-p', '127.0.0.1:8086:8085', '-e', 'FLAGGUARD_STATE_FILE=/tmp/shadow-state.json',
            'flagguard', '-mode', 'shadow')
        started = True
        assert wait('HEALTHY')['mode'] == 'shadow'
        print('SHADOW HEALTHY: isolated controller ready', flush=True)
        call('/api/demo/chaos', {'enabled': True})
        triggered = wait('SHADOW_TRIGGERED')
        assert triggered['flag_enabled'] is True, 'shadow disabled the flag'
        print('SHADOW_TRIGGERED: proposed rollback; flag remains enabled', flush=True)
        # Observe several additional evaluations to catch repeated decisions.
        time.sleep(20)
        state = call('/api/status')['guardrails'][0]
        assert state['status'] == 'SHADOW_TRIGGERED' and state['flag_enabled'] is True
        events = call('/api/events')
        decisions = [e for e in events if e['kind'] == 'WOULD_ROLLBACK']
        assert len(decisions) == 1, 'expected one decision for a continuous breach'
        assert not any(e['kind'] == 'ROLLBACK' for e in events)
        assert not decisions[0].get('change'), 'shadow decision contains a mutation'
        try:
            call('/api/reset/checkout_flow', {})
        except urllib.error.HTTPError as exc:
            assert 400 <= exc.code < 500, 'unexpected reset rejection'
        else:
            raise AssertionError('shadow mode accepted reset')
        result = {'passed': True, 'flag_remained_enabled': True,
                  'continuous_breach_decisions': len(decisions), 'reset_rejected': True,
                  'decision': decisions[0]}
    finally:
        if started:
            call('/api/demo/chaos', {'enabled': False})
            wait('HEALTHY')
            clean = True
            print('SHADOW HEALTHY: fault off; restoring normal controller', flush=True)
        else:
            clean = True  # No chaos request was made.
        if clean:
            if started:
                subprocess.run(['docker', 'rm', '-f', name], check=True)
            subprocess.run(['docker', 'start', controller], check=True)
    smoke.wait_for('HEALTHY')
    result['final_state'] = 'HEALTHY'
    path = root / 'tmp' / 'shadow-smoke-result.json'
    path.parent.mkdir(exist_ok=True)
    path.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
