#!/usr/bin/env python3
"""Exercise only loopback Caddy routes and sanitized headers using synthetic data."""
import argparse
import http.client
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
from pathlib import Path
import socket
import subprocess
import tempfile
import threading
import time


class EchoHeaders(BaseHTTPRequestHandler):
    grant_allowed = False
    auth_requests = []
    asset_requests = 0

    def do_GET(self):
        headers = {name: self.headers.get_all(name, []) for name in
            ('X-Forwarded-For', 'X-Real-IP', 'CF-Connecting-IP', 'Authorization', 'Cookie', 'X-Api-Key')}
        status = 200
        if self.path.startswith('/api/v1/internal/domestic-ip/access'):
            self.__class__.auth_requests.append({'path': self.path, 'headers': headers})
            status = 200 if self.__class__.grant_allowed else 403
        elif self.path.startswith('/assets/'):
            self.__class__.asset_requests += 1
        body = json.dumps({**headers, 'path': self.path}).encode()
        self.send_response(status)
        self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *args):
        pass


def free_port():
    with socket.socket() as sock:
        sock.bind(('127.0.0.1', 0))
        return sock.getsockname()[1]


def request(port, path, host='preview.example.test'):
    conn = http.client.HTTPConnection('127.0.0.1', port, timeout=3)
    conn.putrequest('GET', path, skip_host=True)
    conn.putheader('Host', host)
    conn.putheader('X-Forwarded-For', '1.0.1.8')
    conn.putheader('X-Forwarded-For', '8.8.8.8')
    conn.putheader('X-Real-IP', '1.0.1.8')
    conn.putheader('CF-Connecting-IP', '1.0.1.8')
    conn.putheader('Authorization', 'Bearer synthetic-local-only')
    conn.putheader('Cookie', 'session=synthetic-local-only')
    conn.putheader('X-Api-Key', 'synthetic-local-only')
    conn.endheaders()
    response = conn.getresponse()
    result = response.status, response.read()
    conn.close()
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--caddy', type=Path, required=True)
    args = parser.parse_args()
    backend = ThreadingHTTPServer(('127.0.0.1', 0), EchoHeaders)
    thread = threading.Thread(target=backend.serve_forever, daemon=True)
    thread.start()
    port = free_port()
    process = None
    temporary = tempfile.TemporaryDirectory(prefix='domestic-ip-caddy-')
    try:
        root = Path(temporary.name)
        # 127/8 here is solely a synthetic local test condition. The real
        # exporter deliberately refuses to put loopback in the policy.
        config = '''{
 admin off
 auto_https off
}
(domestic_ip_other_sites) {
 @domestic_ip_denied {
  remote_ip 127.0.0.1/32
  not remote_ip 1.0.1.8
 }
 respond @domestic_ip_denied "Access denied" 403
}
(domestic_ip_sub2api_assets) {
 forward_auth 127.0.0.1:BACKEND {
  uri /api/v1/internal/domestic-ip/access?
  header_up X-Forwarded-For {http.request.remote.host}
  header_up X-Real-IP {http.request.remote.host}
  header_up -CF-Connecting-IP
  header_up -Authorization
  header_up -Cookie
  header_up -X-Api-Key
  header_up -X-Goog-Api-Key
 }
}
(domestic_ip_block_public_internal) {
 @domestic_ip_internal path /api/v1/internal/domestic-ip/access /api/v1/internal/domestic-ip/access/*
 respond @domestic_ip_internal "Not found" 404
}
http://:PORT {
 bind 127.0.0.1
 route {
 import domestic_ip_block_public_internal
 @unexpected_host not host preview.example.test
 respond @unexpected_host "Unknown host" 403
 handle /assets/* {
  route {
   import domestic_ip_sub2api_assets
   reverse_proxy 127.0.0.1:BACKEND
  }
 }
 handle_path /other/* {
  route {
   import domestic_ip_other_sites
   reverse_proxy 127.0.0.1:BACKEND
  }
 }
 handle {
  reverse_proxy 127.0.0.1:BACKEND {
   header_up X-Forwarded-For {http.request.remote.host}
   header_up X-Real-IP {http.request.remote.host}
   header_up -CF-Connecting-IP
  }
 }
 }
}
'''.replace('PORT', str(port)).replace('BACKEND', str(backend.server_port))
        config_path = root / 'Caddyfile'
        config_path.write_text(config)
        with (root / 'caddy.log').open('wb') as log:
            flags = getattr(subprocess, 'CREATE_NO_WINDOW', 0)
            checked = subprocess.run([str(args.caddy.resolve()), 'validate', '--config', str(config_path)],
                stdout=log, stderr=log, timeout=15, creationflags=flags)
            if checked.returncode:
                raise RuntimeError('synthetic Caddy config validation failed')
            process = subprocess.Popen([str(args.caddy.resolve()), 'run', '--config', str(config_path)],
                stdout=log, stderr=log, creationflags=flags)
            deadline = time.monotonic() + 8
            while True:
                try:
                    with socket.create_connection(('127.0.0.1', port), timeout=.2):
                        break
                except OSError:
                    if process.poll() is not None or time.monotonic() >= deadline:
                        raise RuntimeError('synthetic Caddy did not start')
                    time.sleep(.1)
            status, _ = request(port, '/other/file')
            assert status == 403, 'other service bypassed real-peer rule using spoofed header'
            status, body = request(port, '/login')
            assert status == 200, 'login became unreachable due to shared-host branch rule'
            headers = json.loads(body)
            assert headers['X-Forwarded-For'] == ['127.0.0.1'], 'XFF did not overwrite duplicate client values'
            assert headers['X-Real-IP'] == ['127.0.0.1'], 'X-Real-IP did not overwrite client value'
            assert headers['CF-Connecting-IP'] == [], 'CF-Connecting-IP was not removed'
            status, _ = request(port, '/login', host='unknown.example.test')
            assert status == 403, 'unknown host reached fallback app'
            status, _ = request(port, '/api/v1/internal/domestic-ip/access')
            assert status == 404 and not EchoHeaders.auth_requests, 'public caller reached internal grant lookup'
            status, _ = request(port, '/assets/image.png?X-Amz-Signature=synthetic')
            assert status == 403 and EchoHeaders.asset_requests == 0, 'ungranted source reached object storage'
            EchoHeaders.grant_allowed = True
            status, body = request(port, '/assets/image.png?X-Amz-Signature=synthetic')
            assert status == 200 and EchoHeaders.asset_requests == 1, 'granted source could not reach signed asset'
            assert json.loads(body)['path'] == '/assets/image.png?X-Amz-Signature=synthetic', 'storage signed URI was changed'
            assert len(EchoHeaders.auth_requests) == 2
            for auth in EchoHeaders.auth_requests:
                assert auth['path'] == '/api/v1/internal/domestic-ip/access', 'signed query leaked to IP policy endpoint'
                assert auth['headers']['X-Forwarded-For'] == ['127.0.0.1'], 'asset policy used spoofed XFF'
                for name in ('CF-Connecting-IP', 'Authorization', 'Cookie', 'X-Api-Key'):
                    assert auth['headers'][name] == [], 'credential leaked to IP policy endpoint'
            process.terminate()
            process.wait(timeout=5)
            process = None
        print(json.dumps({'caddy_config': 'valid', 'same_host_other_service': 'denied',
            'login': 'reachable', 'duplicate_spoofed_headers': 'overwritten',
                'unknown_host': 'denied', 'signed_assets': 'grant required and original URI preserved',
                'policy_request_credentials': 'removed', 'public_policy_endpoint': 'denied',
                'scope': 'synthetic loopback only'}, indent=2))
    finally:
        if process is not None:
            process.terminate()
            process.wait(timeout=5)
        backend.shutdown()
        backend.server_close()
        thread.join(timeout=2)
        temporary.cleanup()


if __name__ == '__main__':
    main()
