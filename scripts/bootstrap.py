#!/usr/bin/env python3
"""Provision only this project's local Flagsmith demo through its public management API."""
import json
import os
from pathlib import Path
import secrets
import sys
import time
import urllib.error
import urllib.request

ROOT = Path(__file__).resolve().parent.parent
ENV = ROOT / '.env'
BASE = 'http://localhost:8000'
values = dict(line.split('=', 1) for line in ENV.read_text().splitlines() if line and not line.startswith('#') and '=' in line)

def save(**updates):
    values.update({key: str(value) for key, value in updates.items()})
    lines = ENV.read_text().splitlines()
    seen = set()
    for i, line in enumerate(lines):
        key = line.split('=', 1)[0]
        if key in updates:
            lines[i] = key + '=' + values[key]
            seen.add(key)
    lines.extend(key + '=' + values[key] for key in updates if key not in seen)
    temporary = ENV.with_suffix('.tmp')
    fd = os.open(temporary, os.O_CREAT | os.O_TRUNC | os.O_WRONLY, 0o600)
    with os.fdopen(fd, 'w') as file:
        file.write('\n'.join(lines) + '\n')
    temporary.replace(ENV)

token = ''
def api(method, path, body=None):
    req = urllib.request.Request(BASE + path, method=method, data=json.dumps(body).encode() if body is not None else None)
    req.add_header('Content-Type', 'application/json')
    if token:
        req.add_header('Authorization', 'Token ' + token)
    try:
        with urllib.request.urlopen(req, timeout=40) as response:
            raw = response.read()
            return json.loads(raw) if raw else {}
    except urllib.error.HTTPError as error:
        detail = error.read().decode()[:1000]
        for key, value in values.items():
            if value and ('TOKEN' in key or 'PASSWORD' in key or 'SECRET' in key):
                detail = detail.replace(value, '[redacted]')
        raise RuntimeError(f'{method} {path}: HTTP {error.code}: {detail}') from None

def listing(path):
    data = api('GET', path)
    if isinstance(data, list):
        return data
    if data.get('next'):
        raise RuntimeError('Unexpected pagination in local demo; use a fresh local instance.')
    return data.get('results', [])

def ensure(path, name, body):
    found = [item for item in listing(path) if item.get('name') == name]
    if len(found) > 1:
        raise RuntimeError(f'Ambiguous local resource: {name}')
    return found[0] if found else api('POST', path, dict(body, name=name))

def main():
    global token
    print('Waiting for local Flagsmith…', flush=True)
    deadline = time.monotonic() + 600
    while True:
        try:
            version = api('GET', '/version/')
            break
        except (OSError, RuntimeError):
            if time.monotonic() > deadline:
                raise RuntimeError('Flagsmith did not become ready. Run make logs.')
            time.sleep(3)
    fresh = version.get('self_hosted_data', {}).get('has_users') is False
    email = values.get('FLAGSMITH_LOCAL_EMAIL')
    password = values.get('FLAGSMITH_LOCAL_PASSWORD')
    if not fresh and not (email and password):
        raise RuntimeError('This instance already has users. Set FLAGSMITH_LOCAL_EMAIL and FLAGSMITH_LOCAL_PASSWORD in .env to your local account, or use manual setup in README.')
    if fresh:
        email = email or 'demo@flagguard.local'
        password = password or secrets.token_urlsafe(24)
        save(FLAGSMITH_LOCAL_EMAIL=email, FLAGSMITH_LOCAL_PASSWORD=password)
        account = api('POST', '/api/v1/auth/users/', {'email': email, 'password': password, 're_password': password, 'first_name': 'FlagGuard', 'last_name': 'Demo', 'superuser': True})
        token = account.get('key', '')
    if not token:
        token = api('POST', '/api/v1/auth/login/', {'email': email, 'password': password})['key']
    org = ensure('/api/v1/organisations/', 'FlagGuard Demo', {})
    project = ensure('/api/v1/projects/?organisation=' + str(org['id']), 'FlagGuard', {'organisation': org['id']})
    environment = ensure('/api/v1/environments/?project=' + str(project['id']), 'Development', {'project': project['id']})
    key = environment['api_key']
    save(FLAGSMITH_ENV_KEY=key)
    prefix = '/api/v1/environments/' + key
    if not values.get('FLAGSMITH_SERVER_KEY'):
        sdk_key = ensure(prefix + '/api-keys/', 'FlagGuard checkout SDK', {'active': True})
        if not sdk_key['active']:
            raise RuntimeError('The local checkout SDK key is inactive; inspect it before re-enabling.')
        save(FLAGSMITH_SERVER_KEY=sdk_key['key'])
    if not values.get('FLAGSMITH_ADMIN_TOKEN'):
        admin = api('POST', f"/api/v1/organisations/{org['id']}/master-api-keys/", {'name': 'FlagGuard local rollback', 'is_admin': True})
        save(FLAGSMITH_ADMIN_TOKEN=admin['key'])
    warehouse_path = prefix + '/warehouse-connections/'
    warehouses = listing(warehouse_path)
    warehouse = warehouses[0] if warehouses else api('POST', warehouse_path, {'warehouse_type': 'postgres', 'config': {'source': 'flagsmith_database'}})
    if warehouse['status'] != 'connected':
        raise RuntimeError('Flagsmith database warehouse is not connected: ' + warehouse['status'])
    feature = ensure(f"/api/v1/projects/{project['id']}/features/", 'checkout_flow', {
        'type': 'MULTIVARIATE', 'initial_value': 'control', 'default_enabled': True,
        'description': 'FlagGuard synthetic checkout experiment',
        'multivariate_options': [{'type': 'unicode', 'string_value': 'variant_b', 'default_percentage_allocation': 50}],
    })
    options = feature.get('multivariate_options', [])
    variant = next((option for option in options if option.get('string_value') == 'variant_b'), None)
    if not variant:
        raise RuntimeError('checkout_flow exists without the expected variant_b option; inspect it in Flagsmith.')
    metric = ensure(prefix + '/experiment-metrics/', 'Checkout completed', {
        'description': 'Synthetic successful checkout event', 'aggregation': 'occurrence', 'direction': 'up',
        'definition': {'version': 1, 'event': 'checkout_completed'},
    })
    experiment = ensure(prefix + '/experiments/', 'Checkout V2 safety demo', {
        'feature': feature['id'], 'hypothesis': 'A checkout variant must preserve reliability while completing purchases.',
        'metrics': [{'metric': metric['id'], 'expected_direction': 'increase'}],
        'experiment_rollout': {'enabled': True, 'rollout_percentage': 100, 'feature_state_value': {'type': 'string', 'value': 'control'},
            'multivariate_feature_state_values': [{'multivariate_feature_option': variant['id'], 'percentage_allocation': 50}]},
    })
    save(FLAGSMITH_EXPERIMENT_ID=experiment['id'])
    if experiment['status'] == 'created':
        api('POST', prefix + f"/experiments/{experiment['id']}/start/", {})
    elif experiment['status'] != 'running':
        raise RuntimeError('Demo experiment exists but is not running. Inspect it before restarting.')
    data = ROOT / 'data'
    data.mkdir(exist_ok=True)
    login = data / 'local-login.txt'
    fd = os.open(login, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o600)
    with os.fdopen(fd, 'w') as file:
        file.write(f'Local Flagsmith: {BASE}\nEmail: {email}\nPassword: {password}\n')
    print('Local account, flag, warehouse, conversion metric and running experiment are ready.')
    print('Credentials are in data/local-login.txt; integration keys are saved privately in .env.')

if __name__ == '__main__':
    try:
        main()
    except Exception as error:
        print('Setup stopped: ' + str(error), file=sys.stderr)
        sys.exit(1)
