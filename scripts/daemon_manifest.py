"""Validated scenario ownership and batch selection shared by daemon tooling."""
import json
from pathlib import Path
import re

ROOT = Path(__file__).resolve().parents[1]
PATH = ROOT / 'internal/e2e/testdata/claude-scenarios.json'


def keys(value, expected):
    if not isinstance(value, dict) or set(value) != set(expected.split()):
        raise ValueError('Manifest contains missing or unknown fields')


def names(value, pattern, nonempty=False):
    if not isinstance(value, list) or (nonempty and not value) or any(not isinstance(x, str) or not re.fullmatch(pattern, x) for x in value) or len(set(value)) != len(value):
        raise ValueError('Invalid, empty or duplicate manifest names')


def load(path=PATH):
    value = json.loads(Path(path).read_text())
    keys(value, 'version provider_roots scenarios batches reviews')
    if type(value['version']) is not int or value['version'] != 1 or not isinstance(value['scenarios'], list) or not value['scenarios']:
        raise ValueError('Unsupported or empty scenario manifest')
    names(value['provider_roots'], r'[A-Za-z_][A-Za-z0-9_]*', True)
    seen = set()
    for row in value['scenarios']:
        keys(row, 'name stdio_permission initial_yolo claude_args live_tests retained_live live_contracts')
        if not isinstance(row['name'], str) or not re.fullmatch(r'[a-z][a-z0-9-]*', row['name']) or row['name'] in seen:
            raise ValueError('Invalid or duplicate scenario')
        seen.add(row['name'])
        if type(row['stdio_permission']) is not bool or type(row['initial_yolo']) is not bool:
            raise ValueError('Permission configuration must be explicit')
        if not isinstance(row['claude_args'], list) or any(not isinstance(arg, str) or not arg.strip() for arg in row['claude_args']):
            raise ValueError('Claude arguments must be explicit nonempty strings')
        names(row['live_tests'], r'Test[A-Za-z0-9_]+')
        if not isinstance(row['retained_live'], list) or not row['retained_live'] or any(not isinstance(x, str) or not x.strip() for x in row['retained_live']):
            raise ValueError('Retained live obligations must be explicit')
        if not isinstance(row['live_contracts'], dict) or set(row['live_contracts']) != set(row['live_tests']) or any(not isinstance(x, str) or not re.fullmatch(r'[a-f0-9]{64}', x) for x in row['live_contracts'].values()):
            raise ValueError('Live ownership must retain reviewed dependency fingerprints')
    if not isinstance(value['batches'], dict) or not isinstance(value['reviews'], dict):
        raise ValueError('Invalid batches or reviews')
    for name, selection in value['batches'].items():
        if name == 'all' or not re.fullmatch(r'[a-z][a-z0-9-]*', name):
            raise ValueError('Invalid batch name')
        names(selection, r'[a-z][a-z0-9-]*', True)
        if set(selection) - seen:
            raise ValueError('Batch names an unknown scenario')
    for name, review in value['reviews'].items():
        if not re.fullmatch(r'Test[A-Za-z0-9_]+', name):
            raise ValueError('Invalid reviewed test name')
        keys(review, 'disposition reason contract_sha256')
        if review['disposition'] not in ('candidate', 'needs_checkpoint', 'needs_harness', 'live_only', 'control') or not isinstance(review['reason'], str) or not review['reason'].strip() or not isinstance(review['contract_sha256'], str) or not re.fullmatch(r'[a-f0-9]{64}', review['contract_sha256']):
            raise ValueError('Invalid boundary review')
    return value


def select(manifest, batch=None, scenarios=None):
    all_names = [row['name'] for row in manifest['scenarios']]
    if batch and scenarios:
        raise ValueError('Select a batch or individual scenarios')
    if batch:
        if batch != 'all' and batch not in manifest['batches']:
            raise ValueError('Unknown batch')
        scenarios = all_names if batch == 'all' else manifest['batches'][batch]
    if scenarios is None:
        return all_names
    if not scenarios or len(set(scenarios)) != len(scenarios) or set(scenarios) - set(all_names):
        raise ValueError('Unknown, empty or duplicate scenario selection')
    return [name for name in all_names if name in scenarios]
