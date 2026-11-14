import copy
import hashlib
import json
import unittest

from export_policy import build_outputs, decode_element


def fixture():
    return {'nftables': [
        {'set': {'family': 'inet', 'table': 'sub2api_cn_block', 'name': 'cn_ipv4', 'type': 'ipv4_addr',
                 'elem': [{'prefix': {'addr': '1.0.1.0', 'len': 24}}, {'range': ['1.0.2.0', '1.0.2.255']}]}},
        {'set': {'family': 'inet', 'table': 'sub2api_cn_block', 'name': 'cn_ipv6', 'type': 'ipv6_addr',
                 'elem': ['240e::/20']}},
        {'set': {'family': 'inet', 'table': 'sub2api_cn_block', 'name': 'allow_ipv4', 'type': 'ipv4_addr',
                 'elem': [{'elem': {'val': '1.0.1.8', 'comment': 'synthetic fixture'}}]}},
    ]}


class ExportTests(unittest.TestCase):
    def test_same_snapshot_pins_caddy(self):
        data, caddy, manifest = build_outputs(fixture(), generated_at='2026-11-13T00:00:00Z')
        self.assertEqual(hashlib.sha256(data).hexdigest(), manifest['snapshot_sha256'])
        self.assertEqual(hashlib.sha256(caddy).hexdigest(), manifest['caddy_sha256'])
        self.assertIn(manifest['snapshot_sha256'].encode(), caddy)
        snapshot = json.loads(data)
        self.assertEqual(snapshot['manual_ips'], ['1.0.1.8'])
        self.assertIn(b'not remote_ip 1.0.1.8', caddy)
        self.assertEqual(manifest['ipv6_prefixes'], 1)

    def test_separate_element_records_and_empty_manual_set(self):
        source = fixture()
        source['nftables'][2]['set']['elem'] = []
        data, caddy, _ = build_outputs(source)
        self.assertEqual(json.loads(data)['manual_ips'], [])
        self.assertNotIn(b'not remote_ip', caddy)
        source['nftables'].append({'element': {'family': 'inet', 'table': 'sub2api_cn_block',
            'name': 'allow_ipv4', 'elem': ['1.0.1.9']}})
        data, _, _ = build_outputs(source)
        self.assertEqual(json.loads(data)['manual_ips'], ['1.0.1.9'])

    def test_incomplete_policy_and_wrong_family_rejected(self):
        for index in range(3):
            source = fixture()
            del source['nftables'][index]
            with self.assertRaises(ValueError):
                build_outputs(source)
        source = fixture()
        source['nftables'][1]['set']['elem'] = ['1.0.1.0/24']
        with self.assertRaises(ValueError):
            build_outputs(source)

    def test_reserved_cidrs_and_manual_ranges_rejected(self):
        for raw in ['192.168.1.1', '203.0.113.1', '0.0.0.0/0', '1.0.1.0/24']:
            source = fixture()
            source['nftables'][2]['set']['elem'] = [raw]
            with self.assertRaises(ValueError):
                build_outputs(source)
        for raw in ['2001:db8::/32', '2000::/3', 'fc00::/7']:
            source = fixture()
            source['nftables'][1]['set']['elem'] = [raw]
            with self.assertRaises(ValueError):
                build_outputs(source)

    def test_no_temporary_grants_or_unknown_expressions(self):
        for value in [{'elem': {'val': '1.0.1.8', 'timeout': 60}},
                      {'concat': ['1.0.1.8', 443]}, {'range': ['1.0.1.9', '1.0.1.8']}]:
            with self.assertRaises(ValueError):
                decode_element(value)
        source = fixture()
        source['nftables'].append(copy.deepcopy(source['nftables'][0]))
        with self.assertRaises(ValueError):
            build_outputs(source)


if __name__ == '__main__':
    unittest.main()
