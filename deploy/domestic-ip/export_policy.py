#!/usr/bin/env python3
"""Convert a reviewed nft JSON export into one pinned app/Caddy policy version.

No SSH, nft writes, service reloads, network access or production mutations.
Generated manual IPs are confidential operational data; never commit output.
"""
import argparse
import datetime
import hashlib
import ipaddress
import json
from pathlib import Path
import sys

MAX_INPUT_BYTES = 16 << 20
EXCLUDED = tuple(ipaddress.ip_network(s) for s in (
    '0.0.0.0/8', '10.0.0.0/8', '100.64.0.0/10', '127.0.0.0/8',
    '169.254.0.0/16', '172.16.0.0/12', '192.0.0.0/24', '192.0.2.0/24',
    '192.88.99.0/24', '192.168.0.0/16', '198.18.0.0/15', '198.51.100.0/24',
    '203.0.113.0/24', '224.0.0.0/4', '240.0.0.0/4',
    '2001::/23', '2001:db8::/32', '2002::/16', '3fff::/20',
))
IPV6_GLOBAL = ipaddress.ip_network('2000::/3')


def public_network(net):
    if net.version == 6 and not net.subnet_of(IPV6_GLOBAL):
        return False
    return not any(net.version == denied.version and net.overlaps(denied) for denied in EXCLUDED)


def decode_element(value):
    """Support libnftables single addresses, prefixes, intervals and elem wrappers."""
    if isinstance(value, str):
        return [ipaddress.ip_network(value, strict=True)]
    if not isinstance(value, dict):
        raise ValueError('unsupported nft set element')
    if set(value) == {'prefix'}:
        prefix = value['prefix']
        if not isinstance(prefix, dict) or set(prefix) != {'addr', 'len'}:
            raise ValueError('invalid nft prefix element')
        return [ipaddress.ip_network(f"{prefix['addr']}/{prefix['len']}", strict=True)]
    if set(value) == {'range'}:
        bounds = value['range']
        if not isinstance(bounds, list) or len(bounds) != 2:
            raise ValueError('invalid nft interval')
        start, end = map(ipaddress.ip_address, bounds)
        if start.version != end.version or start > end:
            raise ValueError('invalid nft interval order')
        return list(ipaddress.summarize_address_range(start, end))
    if set(value) == {'elem'}:
        elem = value['elem']
        if not isinstance(elem, dict) or 'val' not in elem:
            raise ValueError('invalid nft elem wrapper')
        # Time-limited source grants must never become permanent by accident.
        if any(k in elem for k in ('timeout', 'expires')):
            raise ValueError('cannot convert temporary nft grants to permanent grants')
        if set(elem) - {'val', 'comment', 'counter'}:
            raise ValueError('unsupported nft elem metadata')
        return decode_element(elem['val'])
    raise ValueError('unsupported nft set element object')


def read_sets(source, table):
    if not isinstance(source, dict):
        raise ValueError('expected nftables JSON object')
    names = {'cn_ipv4': 4, 'cn_ipv6': 6, 'allow_ipv4': 4, 'allow_ipv6': 6}
    collected, declared = {}, set()
    entries = source.get('nftables')
    if not isinstance(entries, list):
        raise ValueError('expected nftables JSON array')
    for entry in entries:
        if not isinstance(entry, dict):
            raise ValueError('expected nftables entry object')
        for kind in ('set', 'element'):
            item = entry.get(kind)
            if not isinstance(item, dict) or item.get('family') != 'inet' or item.get('table') != table:
                continue
            name = item.get('name')
            if name not in names:
                continue
            if kind == 'set':
                if name in declared:
                    raise ValueError('duplicate nft set declaration')
                declared.add(name)
                if item.get('type') != ('ipv4_addr' if names[name] == 4 else 'ipv6_addr'):
                    raise ValueError('unexpected nft set type')
                if item.get('timeout') is not None or 'timeout' in item.get('flags', []):
                    raise ValueError('cannot convert temporary nft sets')
            elements = item.get('elem', [])
            if not isinstance(elements, list):
                raise ValueError('expected nft elem array')
            for element in elements:
                for net in decode_element(element):
                    if net.version != names[name] or not public_network(net):
                        raise ValueError('set contains reserved or mismatched IP range')
                    if name.startswith('allow_') and net.prefixlen != net.max_prefixlen:
                        raise ValueError('manual allow set contains a CIDR; review explicitly before conversion')
                    collected.setdefault(name, []).append(net)
                    if len(collected[name]) > 100000:
                        raise ValueError('nft policy entry limit exceeded')
    if not {'cn_ipv4', 'cn_ipv6', 'allow_ipv4'} <= declared:
        raise ValueError('required nft policy sets are missing')
    if not collected.get('cn_ipv4') or not collected.get('cn_ipv6'):
        raise ValueError('both domestic address families must be nonempty')
    return collected


def build_outputs(source, table='sub2api_cn_block', generated_at=None):
    sets = read_sets(source, table)
    cn = [net for name in ('cn_ipv4', 'cn_ipv6') for net in ipaddress.collapse_addresses(sets[name])]
    manual = sorted({str(net.network_address) for name in ('allow_ipv4', 'allow_ipv6') for net in sets.get(name, [])},
                    key=lambda raw: (ipaddress.ip_address(raw).version, int(ipaddress.ip_address(raw))))
    snapshot = {'version': 1,
                'generated_at': generated_at or datetime.datetime.now(datetime.timezone.utc).isoformat(timespec='seconds'),
                'cn_cidrs': [str(net) for net in cn], 'manual_ips': manual}
    data = (json.dumps(snapshot, ensure_ascii=False, indent=2) + '\n').encode()
    if len(data) > MAX_INPUT_BYTES:
        raise ValueError('policy snapshot exceeds app size limit')
    digest = hashlib.sha256(data).hexdigest()
    caddy_lines = [f'# snapshot_sha256={digest}',
                   '# Generated confidential operational data. Import only on non-Sub2API routes.',
                   '(domestic_ip_other_sites) {',
                   '\t@domestic_ip_denied {',
                   '\t\tremote_ip ' + ' '.join(snapshot['cn_cidrs'])]
    if manual:
        caddy_lines.append('\t\tnot remote_ip ' + ' '.join(manual))
    caddy_lines += ['\t}', '\trespond @domestic_ip_denied "Access denied" 403', '}', '',
        '# Import inside the Sub2API reverse_proxy. Only a trusted app region denial aborts transport.',
        '(domestic_ip_transport_denial) {',
        '\t@domestic_ip_transport_denied {',
        '\t\tstatus 403',
        '\t\theader X-Sub2api-Network-Denied domestic-ip',
        '\t}',
        '\thandle_response @domestic_ip_transport_denied {',
        '\t\tabort',
        '\t}',
        '\theader_up -X-Sub2api-Network-Denied',
        '\theader_down -X-Sub2api-Network-Denied',
        '}', '',
        '# Import only on Sub2API-owned signed asset paths. Storage signature checks remain mandatory.',
        '(domestic_ip_sub2api_assets) {',
        '\tforward_auth 127.0.0.1:8080 {',
        '\t\turi /api/v1/internal/domestic-ip/access?',
        '\t\theader_up X-Forwarded-For {http.request.remote.host}',
        '\t\theader_up X-Real-IP {http.request.remote.host}',
        '\t\theader_up -CF-Connecting-IP',
        '\t\theader_up -Authorization',
        '\t\theader_up -Cookie',
        '\t\theader_up -X-Api-Key',
        '\t\theader_up -X-Goog-Api-Key',
        '\t\timport domestic_ip_transport_denial',
        '\t}', '}', '',
        '# Import first inside the public Sub2API route. forward_auth dials the app directly.',
        '(domestic_ip_block_public_internal) {',
        '\t@domestic_ip_internal path /api/v1/internal/domestic-ip/access /api/v1/internal/domestic-ip/access/*',
        '\trespond @domestic_ip_internal "Not found" 404', '}', '']
    caddy = '\n'.join(caddy_lines).encode()
    manifest = {'version': 1, 'snapshot_sha256': digest,
                'caddy_sha256': hashlib.sha256(caddy).hexdigest(),
                'ipv4_prefixes': sum(net.version == 4 for net in cn),
                'ipv6_prefixes': sum(net.version == 6 for net in cn),
                'manual_ip_count': len(manual)}
    return data, caddy, manifest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True, help='local reviewed nft -j export (never committed)')
    parser.add_argument('--output-dir', type=Path, required=True, help='new private generated directory; must not exist')
    parser.add_argument('--table', default='sub2api_cn_block')
    args = parser.parse_args()
    try:
        with args.input.open('rb') as stream:
            raw = stream.read(MAX_INPUT_BYTES + 1)
        if len(raw) > MAX_INPUT_BYTES:
            raise ValueError('nft input size limit exceeded')
        snapshot, caddy, manifest = build_outputs(json.loads(raw), args.table)
        # An immutable version directory prevents partially replacing an active
        # version; activation is a separate reviewed deployment operation.
        args.output_dir.mkdir(mode=0o700, parents=False, exist_ok=False)
        for name, content in (
            ('snapshot.json', snapshot), ('other-sites.caddy', caddy),
            ('manifest.json', (json.dumps(manifest, indent=2) + '\n').encode()),
        ):
            path = args.output_dir / name
            with path.open('xb') as stream:
                stream.write(content)
            path.chmod(0o600)
        print(json.dumps(manifest, indent=2))
        return 0
    except (OSError, ValueError, TypeError, KeyError):
        # Library parse errors can echo actual operational IPs; suppress them.
        print('Policy export failed: input, private output path or policy validation is invalid.', file=sys.stderr)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
