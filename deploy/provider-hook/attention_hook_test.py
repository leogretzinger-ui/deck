"""Content-free native fixture tests; no CLI login or external service."""
import importlib.machinery
import json
import os
from pathlib import Path
import subprocess
import tempfile
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

ADAPTER = Path(__file__).with_name('noisedeck-attention-hook')
hook = importlib.machinery.SourceFileLoader('attention_hook', str(ADAPTER)).load_module()


class TransitionTests(unittest.TestCase):
    def test_parallel_approval_failure_and_lifecycle(self):
        state = {}
        def emit(name, **fields):
            return hook.transition(state, dict(session_id='native', prompt_id='turn', hook_event_name=name, **fields), 'claude')
        self.assertEqual(emit('UserPromptSubmit'), 'started')
        self.assertIsNone(emit('PreToolUse', agent_id='subagent', tool_name='Bash', tool_use_id='sub'))
        self.assertEqual(emit('PreToolUse', agent_type='main', tool_name='AskUserQuestion', tool_use_id='q'), 'input')
        emit('PreToolUse', tool_name='Bash', tool_use_id='a')
        self.assertEqual(emit('PermissionRequest', tool_name='Bash'), 'approval')
        self.assertEqual(emit('PermissionDenied', tool_use_id='a'), 'input')
        self.assertIsNone(emit('Stop'))
        self.assertEqual(emit('PostToolUseFailure', tool_use_id='q'), 'working')
        emit('PreToolUse', tool_name='Bash', tool_use_id='b')
        emit('PreToolUse', tool_name='Bash', tool_use_id='c')
        self.assertEqual(emit('PermissionRequest', tool_name='Bash'), 'unknown')
        self.assertEqual(hook.transition(state, {'session_id':'native','hook_event_name':'SessionEnd'}, 'claude'), 'unknown')
        self.assertEqual(state['tools'], {})

    def test_unknown_missing_ids_stale_turn_and_codex_failure(self):
        state = {}
        def emit(name, turn='one', **fields):
            return hook.transition(state, dict(session_id='native', turn_id=turn, hook_event_name=name, **fields), 'codex')
        self.assertIsNone(emit('PreToolUse', tool_name='request_user_input', tool_use_id='q'))
        emit('UserPromptSubmit')
        self.assertEqual(emit('PreToolUse', tool_name='request_user_input', tool_use_id='q'), 'input')
        # Pinned Codex has no post-failure callback: a failed input stays pending
        # until an authoritative event, rather than inferring a resolution.
        self.assertIsNone(emit('Stop'))
        self.assertEqual(next(iter(state['tools'].values()))['pending'], 'input')
        emit('UserPromptSubmit', turn='two')
        self.assertIsNone(emit('NativeCompletion', turn='one'))
        self.assertIsNone(emit('UserPromptSubmit', turn='one'))
        self.assertEqual(emit('NativeCompletion', turn='two'), 'completed')
        self.assertIsNone(emit('NativeCompletion', turn='two'))
        self.assertEqual(emit('PreToolUse', turn='two', tool_name='shell', tool_use_id='continued'), 'working')
        self.assertEqual(emit('NativeCompletion', turn='two'), 'completed')

    def test_failed_start_is_durably_replayed(self):
        received = []
        fail = [True]
        reject = [False]
        class Receiver(BaseHTTPRequestHandler):
            def log_message(self, *args): pass
            def do_POST(self):
                event = json.loads(self.rfile.read(int(self.headers['Content-Length'])))
                if fail[0]: self.send_response(503)
                elif reject[0]:
                    reject[0] = False
                    self.send_response(409)
                else:
                    received.append(event)
                    self.send_response(204)
                self.end_headers()
        server = ThreadingHTTPServer(('127.0.0.1', 0), Receiver)
        threading.Thread(target=server.serve_forever, daemon=True).start()
        try:
            with tempfile.TemporaryDirectory() as directory:
                env = dict(os.environ, NOISEDECK_ATTENTION_DIR=directory, NOISEDECK_SESSION_HOOK_TOKEN='a'*32,
                           NOISEDECK_SESSION_HOOK_URL=f'http://127.0.0.1:{server.server_port}/api/hooks/capture', NOISEDECK_SESSION_HOOK_SOCKET='')
                def emit(name, **fields):
                    event = dict(hook_event_name=name, session_id='native', prompt_id='one', cwd='/tmp')
                    event.update(fields)
                    result = subprocess.run(['python3', str(ADAPTER), 'claude'], input=json.dumps(event), text=True, env=env, capture_output=True, timeout=15)
                    self.assertEqual((result.returncode,result.stdout,result.stderr), (0,'',''))
                emit('UserPromptSubmit', prompt='PRIVATE_CONTENT')
                self.assertEqual(received, [])
                fail[0] = False
                emit('PreToolUse', tool_name='AskUserQuestion', tool_use_id='q', tool_input={'question':'PRIVATE_CONTENT'})
                self.assertEqual([event['kind'] for event in received], ['started','input'])
                self.assertLess(received[0]['sequence'], received[1]['sequence'])
                persisted = Path(directory, 'attention.json').read_text()
                self.assertNotIn('PRIVATE_CONTENT', persisted)
                self.assertNotIn('a'*32, persisted)
                self.assertEqual(json.loads(persisted)['outbox'], [])
                fail[0] = True
                emit('UserPromptSubmit', prompt_id='two')
                # A native /clear must not replay the old native's failed head.
                fail[0] = False
                emit('UserPromptSubmit', session_id='successor', prompt_id='three')
                self.assertEqual(received[-1]['session_id'], 'successor')
                self.assertEqual(json.loads(Path(directory, 'attention.json').read_text())['outbox'], [])
                # A permanent conflict is discarded so subsequent state can flow.
                reject[0] = True
                emit('PreToolUse', session_id='successor', prompt_id='three', tool_name='Bash', tool_use_id='x')
                emit('PostToolUse', session_id='successor', prompt_id='three', tool_use_id='x')
                self.assertEqual(received[-1]['kind'], 'working')

        finally:
            server.shutdown(); server.server_close()


if __name__ == '__main__':
    unittest.main()
