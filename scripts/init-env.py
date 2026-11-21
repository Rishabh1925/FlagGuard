#!/usr/bin/env python3
"""Generate secrets once without printing them or overwriting existing credentials."""
from pathlib import Path
import secrets
import os

root = Path(__file__).resolve().parent.parent
dest = root / '.env'
if dest.exists():
    print('.env already exists; left unchanged.')
else:
    text = (root / '.env.example').read_text()
    for name in ('POSTGRES_PASSWORD', 'DJANGO_SECRET_KEY', 'WAREHOUSE_CREDENTIALS_SECRET', 'DEMO_CONTROL_TOKEN'):
        text = text.replace(name + '=\n', name + '=' + secrets.token_hex(32) + '\n')
    fd = os.open(dest, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(fd, 'w') as file:
        file.write(text)
    print('Created private .env. Next: make infra')
