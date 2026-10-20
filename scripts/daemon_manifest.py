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
        keys(row, 'name stdio_permission initial_yolo claude_args live_tests retained_live live_contracts comparison_policy boundary')
        if not isinstance(row['name'], str) or not re.fullmatch(r'[a-z][a-z0-9-]*', row['name']) or row['name'] in seen:
            raise ValueError('Invalid or duplicate scenario')
        seen.add(row['name'])
        if type(row['stdio_permission']) is not bool or type(row['initial_yolo']) is not bool:
            raise ValueError('Permission configuration must be explicit')
        if not isinstance(row['claude_args'], list) or any(not isinstance(arg, str) or not arg.strip() for arg in row['claude_args']):
            raise ValueError('Claude arguments must be explicit nonempty strings')
        pool_roots = {
            'pool-model-change': 'TestInteractiveStream_InBandModelChange_LiveChildReportsNewModel',
            'pool-bypass-revoke': 'TestInteractiveStream_InBandBypassRevoke_LiveChildReportsDefaultMode',
            'pool-bypass-escalate': 'TestInteractiveStream_InBandBypassEscalate_LiveChildReportsBypassMode',
            'pool-posture-gate': 'TestInteractiveStream_SpawnPostureGate_LiveChildAcksAndTurnFlows',
        }
        suite_roots = {
            'conversation-default': 'TestInteractivePerConversationLiveness_Default',
            'conversation-workspace': 'TestInteractivePerConversationLiveness_WorkspaceFolder',
            'conversation-turn-state': 'TestInteractivePerConversationTurnStateLiveness',
            'conversation-prompt-argv': 'TestInteractiveSystemPromptFile_LiveSpawnArgv',
        }
        cli_roots = {'cli-prompt-ascii': 'TestRealClaude_PromptFidelity', 'cli-prompt-unicode': 'TestRealClaude_PromptFidelity_Unicode', 'cli-doctor': 'TestRealClaude_DoctorPoisoningRegression', 'cli-bash-nonzero': 'TestRealClaude_BashTool_NonZeroExit', 'cli-max-turns': 'TestRealClaude_MaxTurnsHonored', 'cli-large-prompt': 'TestRealClaude_LargePromptNearContextWindow', 'cli-long-session': 'TestRealClaude_LongSessionJSONLIntegrity', 'cli-tool-loop': 'TestRealClaude_ToolLoopIntegrity', 'cli-role-po': 'TestRealClaude_PO_RoleLoop', 'cli-role-architect': 'TestRealClaude_Architect_RoleLoop', 'cli-role-developer': 'TestRealClaude_Developer_RoleLoop', 'cli-role-review': 'TestRealClaude_CodeReview_RoleLoop', 'cli-role-documentation': 'TestRealClaude_Documentation_RoleLoop'}
        runner_roots = {'runner-applied-settings': 'TestRealClaudeAppliedSettingsBeforeFirstTurn', 'runner-compaction': 'TestRealClaude_CompactingEdges'}
        recovery_roots = {'daemon-session-recovery':'TestInteractiveSessionErrorRecovery'}
        lifecycle_roots = {'conversation-lifecycle':'TestInteractiveConversationLifecycle', 'session-rotation':'TestInteractiveStreamNewSessionRotatesAndSpawnsFresh'}
        boundary = row['boundary']
        if boundary not in ('daemon_stdio', 'pool_stdio', 'daemon_suite', 'cli_process', 'runner_suite', 'recovery_suite', 'lifecycle_suite') or (boundary == 'pool_stdio') != (row['name'] in pool_roots) or (boundary == 'daemon_suite') != (row['name'] in suite_roots) or (boundary == 'cli_process') != (row['name'] in cli_roots) or (boundary == 'runner_suite') != (row['name'] in runner_roots) or (boundary == 'recovery_suite') != (row['name'] in recovery_roots) or (boundary == 'lifecycle_suite') != (row['name'] in lifecycle_roots):
            raise ValueError('Unsupported recording boundary')
        roots = pool_roots if boundary == 'pool_stdio' else cli_roots if boundary == 'cli_process' else runner_roots if boundary == 'runner_suite' else lifecycle_roots if boundary == 'lifecycle_suite' else recovery_roots if boundary == 'recovery_suite' else suite_roots
        if boundary != 'daemon_stdio' and (row['live_tests'] != [roots[row['name']]] or row['stdio_permission'] or row['initial_yolo'] or row['claude_args']):
            raise ValueError('Original-root recording must own exact configuration/root')
        policy = row['comparison_policy']
        if policy not in ('strict_ordered', 'native_coding_prefix') or (policy == 'native_coding_prefix') != (row['name'] == 'native-suggestions'):
            raise ValueError('Unsupported scenario comparison policy')
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
