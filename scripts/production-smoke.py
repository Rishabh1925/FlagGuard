#!/usr/bin/env python3
"""Exercise the hardened Compose service against the running LOCAL demo.

Uses shadow mode, a unique state volume, temporary credentials, and port 8086.
Never changes the demo's flags or stops its enforcing controller.
"""
import base64
import http.client
import json
import os
from pathlib import Path
import secrets
import socket
import ssl
import shutil
import subprocess
import tempfile
import time
import urllib.error
import urllib.request
import uuid

ROOT = Path(__file__).resolve().parent.parent


def check_tls(work, controller, token):
    """Local test CA only: never installs trust or disables certificate checks."""
    name = controller + '-tls'
    config = work / 'Caddyfile'
    config.write_text('''{
    skip_install_trust
    auto_https disable_redirects
}
https://guard.example.test {
    tls internal
    reverse_proxy ''' + controller + ''':8085
    header Strict-Transport-Security "max-age=31536000"
}
''')
    root = work / 'test-ca.crt'
    image = os.environ.get('FLAGGUARD_TEST_CADDY_IMAGE', 'caddy@sha256:4851e54a46b206517c574821bb5cc67f170d2156041e9fbc4c9193b45e84a75b')
    try:
        subprocess.run(['docker', 'run', '-d', '--name', name, '--network', 'flagguard_default',
                        '-p', '127.0.0.1:8443:443', '-v', str(config) + ':/etc/caddy/Caddyfile:ro',
                        image], check=True, capture_output=True)
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline:
            copied = subprocess.run(['docker', 'cp', name + ':/data/caddy/pki/authorities/local/root.crt',
                                     str(root)], capture_output=True)
            if copied.returncode == 0:
                break
            time.sleep(2)
        else:
            raise RuntimeError('Local TLS proxy did not create its test certificate authority')
        context = ssl.create_default_context(cafile=str(root))
        def request(auth=False):
            with socket.create_connection(('localhost', 8443), timeout=30) as raw:
                with context.wrap_socket(raw, server_hostname='guard.example.test') as conn:
                    headers = 'GET /workspace HTTP/1.1\r\nHost: guard.example.test\r\nConnection: close\r\n'
                    if auth:
                        headers += 'Authorization: Bearer ' + token + '\r\n'
                    conn.sendall((headers + '\r\n').encode())
                    response = http.client.HTTPResponse(conn)
                    response.begin()
                    code, hsts = response.status, response.getheader('Strict-Transport-Security')
                    response.read()
                    return code, hsts
        # The CA file can precede leaf issuance by a few seconds.
        while time.monotonic() < deadline:
            try:
                code, _ = request()
                break
            except (OSError, http.client.HTTPException):
                time.sleep(2)
        else:
            raise RuntimeError('Verified TLS connection failed')
        assert code == 401, 'TLS proxy bypassed operator authentication'
        code, hsts = request(True)
        assert code == 200 and hsts == 'max-age=31536000'
        return {'certificate_chain_and_hostname_verified': True, 'unauthenticated_status': 401,
                'authenticated_status': 200, 'hsts': True,
                'scope': 'Local Caddy test CA; public DNS, ACME issuance and renewal not tested'}
    finally:
        subprocess.run(['docker', 'rm', '-fv', name], check=False, capture_output=True)


def main():
    compose = ['docker', 'compose']
    if subprocess.run(compose + ['version'], capture_output=True).returncode:
        compose = ['docker-compose']
    values = dict(line.split('=', 1) for line in (ROOT / '.env').read_text().splitlines()
                  if line and not line.startswith('#') and '=' in line)
    for key in ['FLAGSMITH_ADMIN_TOKEN', 'FLAGSMITH_ENV_KEY']:
        if not values.get(key):
            raise RuntimeError('Provision the local demo first: missing ' + key)
    (ROOT / 'tmp').mkdir(exist_ok=True)
    result_path = ROOT / 'tmp' / 'production-smoke-result.json'
    result_path.unlink(missing_ok=True)
    work = Path(tempfile.mkdtemp(prefix='production-check-', dir=ROOT / 'tmp'))
    name = 'flagguard-prod-check-' + uuid.uuid4().hex[:10]
    token = secrets.token_urlsafe(40)
    env = dict(os.environ)
    env.update(FLAGGUARD_DOMAIN='guard.example.test', FLAGSMITH_API_URL='http://flagsmith:8000/api/v1',
               PROMETHEUS_URL='http://prometheus:9090',
               FLAGSMITH_EXPERIMENT_ID=values.get('FLAGSMITH_EXPERIMENT_ID', '0'))
    for filename, key, value in [('operator', 'OPERATOR_TOKEN_FILE', token),
                                 ('flagsmith', 'FLAGSMITH_TOKEN_FILE', values['FLAGSMITH_ADMIN_TOKEN']),
                                 ('environment', 'ENVIRONMENT_KEY_FILE', values['FLAGSMITH_ENV_KEY'])]:
        path = work / filename
        path.write_text(value + '\n')
        path.chmod(0o444)  # Private 0700 parent; readable in the non-root container.
        env[key] = str(path)
    policy = work / 'guardrails.yaml'
    # Override mode on the CLI too; this policy must NEVER enforce against shared demo flags.
    policy.write_text((ROOT / 'guardrails.yaml').read_text())
    env['GUARDRAILS_FILE'] = str(policy)
    override = work / 'compose.yml'
    override.write_text('networks:\n  default:\n    name: flagguard_default\n    external: true\n')
    prefix = compose + ['-p', name, '-f', str(ROOT / 'compose.production.yml'), '-f', str(override)]
    def run(*args, **kwargs):
        return subprocess.run(prefix + list(args), env=env, cwd=ROOT, check=True, **kwargs)
    def request(path, auth=None, host=None):
        headers = {}
        if auth:
            headers['Authorization'] = auth
        if host:
            headers['Host'] = host
        req = urllib.request.Request('http://localhost:8086' + path, headers=headers)
        try:
            with urllib.request.urlopen(req, timeout=30) as response:
                return response.status, response.read()
        except urllib.error.HTTPError as exc:
            return exc.code, exc.read()
    try:
        run('config', '--quiet')
        run('run', '-d', '--no-deps', '--name', name, '-p', '127.0.0.1:8086:8085',
            'flagguard', '-mode', 'shadow')
        deadline = time.monotonic() + 240
        while time.monotonic() < deadline:
            try:
                code, _ = request('/readyz', 'Bearer ' + token)
                if code == 200:
                    break
            except OSError:
                pass
            time.sleep(3)
        else:
            raise RuntimeError('Authenticated shadow controller never became ready')
        assert request('/')[0] == 200
        assert request('/api/status')[0] == 401
        assert request('/workspace')[0] == 401
        assert request('/api/report', 'Bearer incorrect')[0] == 401
        assert request('/api/status', 'Bearer ' + token, 'untrusted.example')[0] == 403
        basic = base64.b64encode(('operator:' + token).encode()).decode()
        assert request('/workspace', 'Basic ' + basic)[0] == 200
        code, raw = request('/api/status', 'Bearer ' + token)
        status = json.loads(raw)
        assert code == 200 and status['storage_ok'] and not status['demo_enabled']
        assert all(g['mode'] == 'shadow' for g in status['guardrails'])
        raw_report = request('/api/report', 'Bearer ' + token)[1].decode()
        for value in [token, values['FLAGSMITH_ADMIN_TOKEN'], values['FLAGSMITH_ENV_KEY']]:
            assert value not in raw_report, 'report contains a credential'
        # A second process on a different port must fail before evaluation.
        duplicate = subprocess.run(['docker', 'exec', name, '/app/service', '-listen', ':8099', '-mode', 'shadow'],
                                   capture_output=True, text=True, timeout=30)
        assert duplicate.returncode != 0 and 'state lock failed' in duplicate.stderr
        info = json.loads(subprocess.check_output(['docker', 'inspect', name]))[0]
        assert info['HostConfig']['ReadonlyRootfs']
        assert 'ALL' in info['HostConfig']['CapDrop']
        assert info['Config']['User'] == 'app'
        before_events = json.loads(request('/api/events', 'Bearer ' + token)[1])
        subprocess.run(['docker', 'restart', name], check=True, capture_output=True)
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            try:
                if request('/readyz', 'Bearer ' + token)[0] == 200:
                    break
            except OSError:
                pass
            time.sleep(3)
        else:
            raise RuntimeError('Restarted controller never became ready')
        after_events = json.loads(request('/api/events', 'Bearer ' + token)[1])
        assert before_events and all(event in after_events for event in before_events), 'audit history lost on restart'
        assert request('/api/status')[0] == 401, 'restart lost authentication'
        result = {'passed': True, 'mode': 'shadow', 'authentication': 'Basic and Bearer verified',
                  'unauthorized_status': 401, 'unknown_host_status': 403, 'state_lock_exclusion': True,
                  'read_only_root': True, 'non_root': True, 'mounted_secrets': True,
                  'report_excludes_credentials': True, 'restart_retains_audit_and_auth': True,
                  'tls': check_tls(work, name, token)}
        result_path.write_text(json.dumps(result, indent=2) + '\n')
        print(json.dumps(result, indent=2))
    finally:
        subprocess.run(['docker', 'rm', '-f', name], check=False, capture_output=True)
        # Only the uniquely named test project volume is removed; never production/demo data.
        subprocess.run(['docker', 'volume', 'rm', name + '_guarddata'], check=False, capture_output=True)
        shutil.rmtree(work)


if __name__ == '__main__':
    main()
