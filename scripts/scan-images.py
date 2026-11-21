#!/usr/bin/env python3
"""Scan exported images with pinned Trivy; no Docker socket is shared with it."""
import json
from pathlib import Path
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
SCANNER = 'aquasec/trivy@sha256:8f6bb947dbc528902e11f2f5be52f7b519f6a39d5d148f41166f2040499768fb'


def main():
    out = ROOT / 'tmp' / 'image-scans'
    out.mkdir(parents=True, exist_ok=True)
    failed = False
    for index, target in enumerate(sys.argv[1:] or ['flagguard-flagguard', 'flagguard-demo-app']):
        archive = out / f'image-{index}.tar'
        report = out / f'image-{index}.json'
        report.unlink(missing_ok=True)
        try:
            subprocess.run(['docker', 'save', '-o', str(archive), target], check=True)
            archive.chmod(0o644)  # Image contains application code, not deployment secrets.
            with report.open('w') as handle:
                subprocess.run(['docker', 'run', '--rm', '--read-only', '--cap-drop', 'ALL',
                                '--security-opt', 'no-new-privileges', '--tmpfs', '/tmp:rw,size=512m',
                                '-v', 'flagguard-trivy-cache:/cache',
                                '-v', str(archive) + ':/scan/image.tar:ro', SCANNER,
                                'image', '--quiet', '--cache-dir', '/cache', '--scanners', 'vuln',
                                '--input', '/scan/image.tar', '--format', 'json'], stdout=handle, check=True)
            data = json.loads(report.read_text())
            findings = [v for r in data.get('Results', []) for v in r.get('Vulnerabilities', [])]
            print(f'{target}: {len(findings)} known vulnerabilities; report {report}', flush=True)
            for finding in findings:
                print(f"  {finding['Severity']} {finding['VulnerabilityID']} {finding['PkgName']} "
                      f"{finding['InstalledVersion']} -> {finding.get('FixedVersion', 'unfixed')}")
            failed |= any(v['Severity'] in ('MEDIUM', 'HIGH', 'CRITICAL') for v in findings)
        finally:
            archive.unlink(missing_ok=True)
    if failed:
        raise SystemExit('Release gate failed: medium/high/critical vulnerabilities need review and remediation')


if __name__ == '__main__':
    main()
