#!/usr/bin/env python3
"""Exercise the local demo's real healthy → chaos → rollback → reset loop."""
import json
from pathlib import Path
import time
import urllib.request

BASE = 'http://localhost:8085'
def call(path, body=None, base=BASE):
    request = urllib.request.Request(base + path, data=None if body is None else json.dumps(body).encode(), headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(request, timeout=40) as response:
        return json.load(response)

def wait_for(want, seconds=240):
    until = time.monotonic() + seconds
    previous = None
    while time.monotonic() < until:
        try:
            state = call('/api/status')['guardrails'][0]
        except OSError:
            time.sleep(3)
            continue
        if state['status'] != previous:
            print(state['status'] + ': ' + state['message'], flush=True)
            previous = state['status']
        if state['status'] == want:
            return state
        if state['status'] == 'ACTION_REQUIRED':
            raise RuntimeError(state['message'])
        time.sleep(3)
    raise RuntimeError('Timed out waiting for ' + want)

def main():
    path = Path(__file__).resolve().parent.parent / 'tmp' / 'smoke-result.json'
    path.parent.mkdir(exist_ok=True)
    path.unlink(missing_ok=True)
    wait_for('HEALTHY')
    before = {event['time'] for event in call('/api/events') if event['kind'] == 'ROLLBACK'}
    started = time.monotonic()
    try:
        call('/api/demo/chaos', {'enabled': True})
        rolled = wait_for('ROLLED_BACK')
        elapsed = round(time.monotonic() - started, 1)
        assert rolled['flag_enabled'] is False, 'flag is still enabled'
        # Allow the 2-second SDK environment poll and in-flight requests to drain.
        time.sleep(5)
        for i in range(20):
            checkout = call('/checkout?user_id=smoke_' + str(i), base='http://localhost:8081')
            assert checkout['variant'] == 'control' and checkout['success'], checkout
        events = [event for event in call('/api/events')
                  if event['kind'] == 'ROLLBACK' and event['time'] not in before]
        assert len(events) == 1, 'rollback was duplicated or missing'
        rollback = events[0]
        evidence = rollback['evidence']
        assert evidence['samples'] >= evidence['minimum_samples'], 'missing sample gate evidence'
        assert evidence.get('control', {}).get('variant') == 'control', 'missing control evidence'
        assert all(rule.get('diagnosis') for rule in evidence['rules']), 'missing comparison diagnosis'
        change = rollback['change']
        assert change['default_after_enabled'] is False and change['rollout_after_enabled'] is False
        report = call('/api/report')
        assert report['schema_version'] == 1
        assert any(e.get('evidence') == evidence for e in report['events']), 'report lost decision evidence'
        print('Verified: one rollback, disabled flag, and 20/20 checkout responses on control.', flush=True)
    finally:
        call('/api/demo/chaos', {'enabled': False})
    call('/api/reset/checkout_flow', {})
    recovered = wait_for('HEALTHY')
    assert recovered['flag_enabled'] is True
    assert call('/status', base='http://localhost:8081')['chaos'] is False
    assert any(e.get('evidence') == evidence for e in call('/api/report')['events']), 'reset changed evidence'
    result = {'passed': True, 'chaos_to_verified_rollback_seconds': elapsed, 'control_requests_verified': 20, 'rollback': rollback, 'final_state': recovered['status']}
    path.write_text(json.dumps(result, indent=2) + '\n')
    print(json.dumps(result, indent=2))

if __name__ == '__main__':
    main()
