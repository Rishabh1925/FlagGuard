#!/usr/bin/env python3
"""Package only reviewable source; never local credentials, state or build output."""
import hashlib
import json
from pathlib import Path
import zipfile

ROOT = Path(__file__).resolve().parent.parent
FILES = ['.dockerignore', '.env.example', '.gitignore', 'Dockerfile', 'LICENSE',
         'Makefile', 'README.md', 'compose.production.yml', 'compose.standalone.yml',
         'docker-compose.yml', 'go.mod', 'go.sum', 'guardrails.yaml', 'prometheus.yml']
DIRECTORIES = ['.github', 'cmd', 'deploy', 'docs', 'internal', 'scripts']
EXCLUDED = {'__pycache__', '.DS_Store'}


def main():
    paths = [ROOT / name for name in FILES]
    for directory in DIRECTORIES:
        paths.extend(p for p in (ROOT / directory).rglob('*')
                     if p.is_file() and not EXCLUDED.intersection(p.parts) and p.suffix != '.pyc')
    paths = sorted(set(paths))
    hashes = {}
    (ROOT / 'tmp').mkdir(exist_ok=True)
    archive = ROOT / 'tmp' / 'flagguard-source.zip'
    with zipfile.ZipFile(archive, 'w', zipfile.ZIP_DEFLATED) as out:
        for path in paths:
            if path.is_symlink() or not path.is_file():
                raise RuntimeError('Missing or symbolic source file: ' + str(path))
            relative = path.relative_to(ROOT).as_posix()
            raw = path.read_bytes()
            hashes[relative] = hashlib.sha256(raw).hexdigest()
            entry = zipfile.ZipInfo('FlagGuard/' + relative, (2000, 1, 1, 0, 0, 0))
            entry.compress_type = zipfile.ZIP_DEFLATED
            entry.external_attr = 0o100644 << 16
            out.writestr(entry, raw)
        manifest = zipfile.ZipInfo('FlagGuard/SOURCE-MANIFEST.json', (2000, 1, 1, 0, 0, 0))
        manifest.compress_type = zipfile.ZIP_DEFLATED
        out.writestr(manifest, json.dumps(hashes, sort_keys=True, indent=2) + '\n')
    checksum = hashlib.sha256(archive.read_bytes()).hexdigest()
    archive.with_suffix('.zip.sha256').write_text(checksum + '  ' + archive.name + '\n')
    print(f'Packaged {len(hashes)} source files: {archive}')
    print('SHA256: ' + checksum)


if __name__ == '__main__':
    main()
