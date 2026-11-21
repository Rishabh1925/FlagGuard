#!/usr/bin/env python3
"""Cross-compile on the host; package for the Docker daemon's architecture."""
import os
from pathlib import Path
import shutil
import subprocess

root = Path(__file__).resolve().parent.parent
os.chdir(root)
arch = subprocess.check_output(['docker', 'info', '--format', '{{.Architecture}}'], text=True).strip()
arch = {'x86_64': 'amd64', 'aarch64': 'arm64'}.get(arch, arch)
if arch not in ('amd64', 'arm64'):
    raise SystemExit('Unsupported Docker architecture: ' + arch)
for target, image in [('flagguard', 'flagguard-flagguard'), ('demo', 'flagguard-demo-app')]:
    dest = root / 'tmp' / 'local-images' / target
    dest.mkdir(parents=True, exist_ok=True)
    print('Building ' + target + ' for linux/' + arch, flush=True)
    env = dict(os.environ, CGO_ENABLED='0', GOOS='linux', GOARCH=arch)
    subprocess.run(['go', 'build', '-trimpath', '-ldflags=-s -w', '-o', str(dest / 'service'), './cmd/' + target], env=env, check=True)
    shutil.copy(root / 'guardrails.yaml', dest / 'guardrails.yaml')
    (dest / 'Dockerfile').write_text('''FROM alpine:3.23
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates && adduser -D -u 10001 app && mkdir /data && chown app:app /data
WORKDIR /app
COPY service /app/service
COPY guardrails.yaml /app/guardrails.yaml
USER app
ENTRYPOINT ["/app/service"]
''')
    subprocess.run(['docker', 'build', '-t', image, str(dest)], check=True)
print('Images ready. Run docker-compose up -d --no-build (or docker compose).')
