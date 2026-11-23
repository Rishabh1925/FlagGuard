#!/usr/bin/env python3
"""Compare the shared-buffer work of two trusted, read-only staging queries."""
import argparse
import json
import os
import subprocess
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

METRIC = 'flagguard_postgres_shared_blocks'


def read_blocks(query):
    # Files are authored by the operator. Keep psql commands and batches out of the probe.
    sql = query.strip().removesuffix(';').strip()
    words = sql.split(None, 1)
    if not words or words[0].upper() != 'SELECT' or ';' in sql or '\\' in sql:
        raise ValueError('Expected one SELECT statement without embedded semicolons or backslashes')
    env = dict(os.environ, PGCONNECT_TIMEOUT='3')
    result = subprocess.run(['psql', '-X', '-w', '-qAt', '-v', 'ON_ERROR_STOP=1', '-f', '-'],
                            input="BEGIN READ ONLY;\nSET LOCAL statement_timeout = '2s';\n"
                                  + 'EXPLAIN (ANALYZE, BUFFERS, TIMING OFF, FORMAT JSON) ' + sql + ';\nROLLBACK;\n',
                            text=True, capture_output=True, env=env, timeout=6, check=True)
    root = json.loads(result.stdout)[0]['Plan']
    return root['Shared Hit Blocks'] + root['Shared Read Blocks']


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--control', required=True, type=Path)
    parser.add_argument('--candidate', required=True, type=Path)
    parser.add_argument('--port', type=int, default=9328, help='Exporter port (default 9328)')
    parser.add_argument('--bind', default='127.0.0.1', help='Exporter listen address')
    args = parser.parse_args()
    queries = {key: getattr(args, key).read_text() for key in ('control', 'candidate')}

    class Handler(BaseHTTPRequestHandler):
        def do_GET(self):
            if self.path != '/metrics':
                self.send_error(404)
                return
            try:
                readings = {arm: read_blocks(query) for arm, query in queries.items()}
            except (OSError, ValueError, KeyError, IndexError, subprocess.SubprocessError):
                self.send_error(503, 'Database measurement unavailable')
                return
            lines = [f'# HELP {METRIC} Shared buffer hits plus reads of one query execution.', f'# TYPE {METRIC} gauge']
            lines.extend(f'{METRIC}{{variant="{arm}"}} {value}' for arm, value in readings.items())
            body = ('\n'.join(lines) + '\n').encode()
            self.send_response(200)
            self.send_header('Content-Type', 'text/plain; version=0.0.4; charset=utf-8')
            self.send_header('Content-Length', str(len(body)))
            self.end_headers()
            self.wfile.write(body)

    with HTTPServer((args.bind, args.port), Handler) as server:
        server.serve_forever()


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, KeyError, IndexError, subprocess.SubprocessError) as err:
        sys.exit(f'Measurement failed: {err}')
