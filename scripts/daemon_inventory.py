"""Evidence-based static inventory. No-path findings are not offline-safety proofs."""
from collections import Counter, deque
import hashlib
import json
from pathlib import Path
import subprocess


def source_functions(root):
    output = subprocess.check_output(['go', 'run', './scripts/daemon-test-inventory', '-directory',
                                      str(root / 'internal/e2e/realclaude')], cwd=root, text=True)
    return json.loads(output)


def closure(nodes, name):
    seen, pending = set(), [name]
    while pending:
        item = pending.pop()
        if item in seen:
            continue
        seen.add(item)
        pending.extend(nodes[item]['references'])
    return seen


def contract_digest(nodes, names):
    value = {name: [nodes[name]['source_sha256'], nodes[name]['context_sha256']] for name in sorted(names)}
    return hashlib.sha256(json.dumps(value, sort_keys=True).encode()).hexdigest()


def provider_path(nodes, name, roots):
    pending = deque([[name]])
    seen = set()
    while pending:
        path = pending.popleft()
        item = path[-1]
        if item in seen:
            continue
        seen.add(item)
        if item in roots or nodes[item]['provider_command']:
            return path
        pending.extend(path + [ref] for ref in nodes[item]['references'])
    return []


def inventory(root, manifest, functions=None):
    functions = source_functions(root) if functions is None else functions
    nodes = {item['name']: item for item in functions}
    tests = {name: item for name, item in nodes.items() if item['test']}
    issues = []
    roots = set(manifest['provider_roots'])
    if roots - nodes.keys():
        issues.append('Provider roots disappeared: ' + ', '.join(sorted(roots - nodes.keys())))
    owners = {}
    contracts = {}
    for scenario in manifest['scenarios']:
        for name in scenario['live_tests']:
            owners.setdefault(name, []).append(scenario['name'])
            contracts[name] = scenario['live_contracts'][name]
    for name, review in manifest['reviews'].items():
        if name in owners:
            issues.append('Review overlaps a shared replay owner: ' + name)
        contracts[name] = review['contract_sha256']
    for name in contracts.keys() - tests.keys():
        issues.append('Reviewed test disappeared: ' + name)
    rows = []
    for name, item in sorted(tests.items()):
        reachable = closure(nodes, name)
        digest = contract_digest(nodes, reachable)
        path = provider_path(nodes, name, roots)
        if name.endswith('HelperProcess'):
            dependency = 'process_helper'
        elif path:
            dependency = 'possible_provider_dependency'
        else:
            dependency = 'no_known_provider_path'
        review = manifest['reviews'].get(name, {})
        disposition = 'shared_replay' if name in owners else review.get('disposition', 'unreviewed')
        if name in contracts and contracts[name] != digest:
            issues.append('Source changed since boundary review: ' + name)
            disposition = 'stale_review'
        rows.append({'name': name, 'file': 'internal/e2e/realclaude/' + item['file'],
                     'selection': 'live-tagged (includes controls and probes)' if item['live_tagged'] else 'untagged',
                     'dependency': dependency, 'provider_path': path,
                     'env_gates': sorted({gate for ref in reachable for gate in nodes[ref]['env_gates']}),
                     'disposition': disposition, 'scenarios': owners.get(name, []),
                     'review_reason': review.get('reason'), 'contract_sha256': digest})
    return {'inventory_kind': 'static source analysis, not executed acceptance or automatic replay eligibility',
            'limitations': 'Package-function references are conservative; methods, dynamic dispatch, generated code and external packages are not resolved. No known provider path is not proof of offline safety.',
            'recorded_scenarios': [entry['name'] for entry in manifest['scenarios']],
            'summary': {'test_declarations': len(rows),
                        'selection': dict(Counter(row['selection'] for row in rows)),
                        'dependency': dict(Counter(row['dependency'] for row in rows)),
                        'disposition': dict(Counter(row['disposition'] for row in rows)),
                        'recorded_scenarios': len(manifest['scenarios']),
                        'shared_live_entries': sum(row['disposition'] == 'shared_replay' for row in rows),
                        'remaining_eligibility_review': sum(row['disposition'] == 'unreviewed' and row['dependency'] == 'possible_provider_dependency' for row in rows),
                        'reviewed_migration_backlog': dict(Counter(row['disposition'] for row in rows if row['disposition'] in ('candidate', 'needs_checkpoint', 'needs_harness'))),
                        'total_replay_eligible': None, 'live_tests_retired': 0},
            'issues': issues, 'existing_tests': rows}
