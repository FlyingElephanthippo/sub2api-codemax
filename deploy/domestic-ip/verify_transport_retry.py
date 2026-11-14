#!/usr/bin/env python3
"""Check generated denial handling with real loopback Caddy over HTTP/1.1 and HTTP/2.

Requires httpx[http2] and cryptography. Uses synthetic responses and a temporary
certificate; it never invokes a model, production service, or paid endpoint.
"""
import argparse
from concurrent.futures import ThreadPoolExecutor
import datetime
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import ipaddress
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time

import httpx
from cryptography import x509
from cryptography.hazmat.primitives import hashes, serialization
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.x509.oid import NameOID

from export_policy import build_outputs

HEADER = 'X-Sub2api-Network-Denied'


class Origin(BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'
    allowed = False
    generation_count = 0
    assets_count = 0
    spoofed_marker_seen = False
    denied_count = 0
    slow_started = threading.Event()
    slow_continue = threading.Event()

    def handle(self):
        try:
            super().handle()
        except (ConnectionResetError, ConnectionAbortedError):
            # Expected when Caddy discards an intercepted upstream response.
            pass

    def do_POST(self):
        self.rfile.read(int(self.headers.get('Content-Length', '0')))
        self.do_GET()

    def do_GET(self):
        cls = self.__class__
        if self.headers.get(HEADER):
            cls.spoofed_marker_seen = True
        status, marker, body = 200, None, b'synthetic-success'
        if self.path in ('/responses', '/api/v1/internal/domestic-ip/access'):
            if not cls.allowed:
                status, marker, body = 403, 'domestic-ip', b'synthetic-region-denial'
                cls.denied_count += 1
            elif self.path == '/responses':
                cls.generation_count += 1
        elif self.path.startswith('/assets/'):
            cls.assets_count += 1
        elif self.path == '/slow':
            cls.slow_started.set()
            cls.slow_continue.wait(timeout=5)
        elif self.path == '/other-forbidden':
            status, body = 403, b'ordinary-business-forbidden'
        elif self.path == '/wrong-marker':
            status, marker = 403, 'other-error'
        elif self.path == '/marker-wrong-status':
            status, marker = 429, 'domestic-ip'
        elif self.path == '/unauthorized':
            status = 401
        elif self.path == '/unavailable':
            status = 503
        elif self.path == '/server-error':
            status = 500
        self.send_response(status)
        if marker:
            self.send_header(HEADER, marker)
        self.send_header('Content-Type', 'text/plain')
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def certificate(folder):
    key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
    name = x509.Name([x509.NameAttribute(NameOID.COMMON_NAME, 'localhost')])
    now = datetime.datetime.now(datetime.timezone.utc)
    cert = (x509.CertificateBuilder().subject_name(name).issuer_name(name)
            .public_key(key.public_key()).serial_number(x509.random_serial_number())
            .not_valid_before(now - datetime.timedelta(minutes=1))
            .not_valid_after(now + datetime.timedelta(days=1))
            .add_extension(x509.SubjectAlternativeName([
                x509.DNSName('localhost'), x509.IPAddress(ipaddress.ip_address('127.0.0.1')),
            ]), critical=False).sign(key, hashes.SHA256()))
    (folder / 'cert.pem').write_bytes(cert.public_bytes(serialization.Encoding.PEM))
    (folder / 'key.pem').write_bytes(key.private_bytes(serialization.Encoding.PEM,
        serialization.PrivateFormat.PKCS8, serialization.NoEncryption()))


def transport_failure(client, path, headers=None, method='POST'):
    try:
        result = client.request(method, path, content='synthetic' if method == 'POST' else None, headers=headers)
    except (httpx.RemoteProtocolError, httpx.ReadError) as error:
        return type(error).__name__
    raise AssertionError(f'Expected transport interruption, received HTTP {result.status_code}')


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--caddy', type=Path, required=True)
    args = parser.parse_args()
    origin = ThreadingHTTPServer(('127.0.0.1', 0), Origin)
    threading.Thread(target=origin.serve_forever, daemon=True).start()
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        port = sock.getsockname()[1]
    process = None
    reports = []
    flags = getattr(subprocess, 'CREATE_NO_WINDOW', 0)
    try:
        with tempfile.TemporaryDirectory(prefix='domestic-ip-transport-') as tmp:
            folder = Path(tmp)
            certificate(folder)
            source = {'nftables': [
                {'set': {'family': 'inet', 'table': 'sub2api_cn_block', 'name': 'cn_ipv4', 'type': 'ipv4_addr', 'elem': ['1.0.1.0/24']}},
                {'set': {'family': 'inet', 'table': 'sub2api_cn_block', 'name': 'cn_ipv6', 'type': 'ipv6_addr', 'elem': ['240e::/20']}},
                {'set': {'family': 'inet', 'table': 'sub2api_cn_block', 'name': 'allow_ipv4', 'type': 'ipv4_addr', 'elem': []}},
            ]}
            _, snippets, _ = build_outputs(source)
            snippets = snippets.decode().replace('127.0.0.1:8080', f'127.0.0.1:{origin.server_port}')
            config = ('{\n admin off\n auto_https off\n}\n' + snippets + f'''
https://localhost:{port} {{
 bind 127.0.0.1
 tls "{(folder / 'cert.pem').as_posix()}" "{(folder / 'key.pem').as_posix()}"
 route {{
  import domestic_ip_block_public_internal
  handle /assets/* {{
   route {{
    import domestic_ip_sub2api_assets
    reverse_proxy 127.0.0.1:{origin.server_port}
   }}
  }}
  handle {{
   reverse_proxy 127.0.0.1:{origin.server_port} {{
    import domestic_ip_transport_denial
   }}
  }}
 }}
}}
''')
            config_path = folder / 'Caddyfile'
            config_path.write_text(config, encoding='utf-8')
            with (folder / 'caddy.log').open('wb') as log:
                checked = subprocess.run([str(args.caddy), 'validate', '--config', str(config_path)],
                    stdout=log, stderr=log, creationflags=flags, timeout=15)
                if checked.returncode:
                    raise RuntimeError((folder / 'caddy.log').read_text())
                process = subprocess.Popen([str(args.caddy), 'run', '--config', str(config_path)],
                    stdout=log, stderr=log, creationflags=flags)
                deadline = time.monotonic() + 8
                while True:
                    try:
                        with socket.create_connection(('127.0.0.1', port), timeout=.2):
                            break
                    except OSError:
                        if process.poll() is not None or time.monotonic() >= deadline:
                            raise RuntimeError('Caddy failed to start')
                        time.sleep(.1)
                for use_h2 in (False, True):
                    Origin.allowed = False
                    with httpx.Client(http2=use_h2, verify=False, trust_env=False,
                                      timeout=6, base_url=f'https://localhost:{port}') as client:
                        response = client.get('/login')
                        assert response.status_code == 200
                        expected_version = 'HTTP/2' if use_h2 else 'HTTP/1.1'
                        assert response.http_version == expected_version
                        before = Origin.generation_count
                        failure = transport_failure(client, '/responses', {HEADER: 'domestic-ip'})
                        assert Origin.generation_count == before
                        for path, status in [('/other-forbidden', 403), ('/wrong-marker', 403),
                                             ('/marker-wrong-status', 429), ('/unauthorized', 401),
                                             ('/unavailable', 503), ('/server-error', 500)]:
                            response = client.get(path)
                            assert response.status_code == status, (path, response.status_code)
                            assert HEADER not in response.headers
                        assert client.get('/api/v1/internal/domestic-ip/access').status_code == 404
                        before_assets = Origin.assets_count
                        asset_failure = transport_failure(client, '/assets/test.png')
                        assert Origin.assets_count == before_assets
                        websocket_failure = transport_failure(client, '/responses',
                            {'Connection': 'Upgrade', 'Upgrade': 'websocket',
                             'Sec-WebSocket-Version': '13', 'Sec-WebSocket-Key': 'dGhlIHNhbXBsZSBub25jZQ=='}, method='GET')
                        parallel_ok = None
                        if use_h2:
                            Origin.slow_started.clear()
                            Origin.slow_continue.clear()
                            with ThreadPoolExecutor(max_workers=1) as executor:
                                slow = executor.submit(client.get, '/slow')
                                assert Origin.slow_started.wait(timeout=3)
                                try:
                                    transport_failure(client, '/responses')
                                finally:
                                    Origin.slow_continue.set()
                                response = slow.result(timeout=5)
                                assert response.http_version == 'HTTP/2' and response.status_code == 200
                                parallel_ok = True
                        # Synthetic bounded retry: denied attempts never dispatch a generation.
                        for attempt in range(3):
                            if attempt == 2:
                                Origin.allowed = True
                            try:
                                response = client.post('/responses', content='one-synthetic-task')
                            except (httpx.RemoteProtocolError, httpx.ReadError):
                                assert attempt < 2
                                continue
                            assert attempt == 2 and response.status_code == 200
                            break
                        assert Origin.generation_count == before + 1
                        assert client.get('/assets/test.png?signature=synthetic').status_code == 200
                        assert Origin.assets_count == before_assets + 1
                        assert not Origin.spoofed_marker_seen
                        reports.append({'protocol': expected_version, 'region_denial': failure,
                            'asset_denial': asset_failure, 'websocket_preflight_denial': websocket_failure,
                            'ordinary_errors_preserved': True, 'bounded_retry_recovers': True,
                            'parallel_http2_response_preserved': parallel_ok,
                            'generation_dispatched_once': True, 'public_internal_path': 404})
                process.terminate()
                process.wait(timeout=5)
                process = None
        print(json.dumps({'status': 'passed', 'synthetic_only': True, 'protocols': reports}, indent=2))
    finally:
        if process is not None:
            process.terminate()
            process.wait(timeout=5)
        origin.shutdown()
        origin.server_close()


if __name__ == '__main__':
    main()
