#!/usr/bin/env bash
# mock-apisix.sh — tiny HTTP stub that returns a valid chat completion response.
# Run this in a separate terminal when developing the shim without real vLLM:
#   bash slurm_templates/mock-apisix.sh
#
# Listens on port 9080 (matches APISIX_URL=http://localhost:9080 in .devcontainer).
# Requires: python3 (available in the devcontainer)

echo "Mock APIsix listening on :9080 — press Ctrl+C to stop"

python3 -c "
import json, time
from http.server import HTTPServer, BaseHTTPRequestHandler

class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print(f'[mock-apisix] {self.address_string()} - {fmt % args}')

    def do_POST(self):
        length = int(self.headers.get('Content-Length', 0))
        body = json.loads(self.rfile.read(length)) if length else {}
        model = body.get('model', 'test-model')
        messages = body.get('messages', [])
        last_msg = messages[-1]['content'] if messages else 'hello'

        resp = {
            'id': 'chatcmpl-dev-' + str(int(time.time())),
            'object': 'chat.completion',
            'model': model,
            'choices': [{
                'index': 0,
                'message': {'role': 'assistant', 'content': f'[mock] echo: {last_msg}'},
                'finish_reason': 'stop',
            }],
            'usage': {
                'prompt_tokens': len(str(messages)),
                'completion_tokens': 10,
                'total_tokens': len(str(messages)) + 10,
            },
        }
        data = json.dumps(resp).encode()
        self.send_response(200)
        self.send_header('Content-Type', 'application/json')
        self.send_header('Content-Length', str(len(data)))
        self.end_headers()
        self.wfile.write(data)

HTTPServer(('0.0.0.0', 9080), Handler).serve_forever()
"
