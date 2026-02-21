#!/usr/bin/env python3
"""spaCy NER sidecar for Engram.

API:
  POST /extract   {"text": "..."} → {"entities": [...], "has_entities": bool, "duration_ms": float}
  GET  /health    → 200 OK
"""
import time
import json
from http.server import BaseHTTPRequestHandler, HTTPServer

import spacy

# Load model once at startup. en_core_web_sm is fast (~12 MB).
# Set SPACY_MODEL env var to use a larger model (e.g. en_core_web_trf).
import os
MODEL = os.environ.get("SPACY_MODEL", "en_core_web_sm")
PORT = int(os.environ.get("NER_PORT", "5001"))

print(f"[ner] loading spaCy model: {MODEL}", flush=True)
nlp = spacy.load(MODEL)
print(f"[ner] model loaded, listening on :{PORT}", flush=True)


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        # Suppress default access log noise; errors still printed
        pass

    def do_GET(self):
        if self.path == "/health":
            self._send(200, {"status": "ok"})
        else:
            self._send(404, {"error": "not found"})

    def do_POST(self):
        if self.path == "/extract":
            length = int(self.headers.get("Content-Length", 0))
            body = self.rfile.read(length)
            try:
                req = json.loads(body)
                text = req.get("text", "")
            except (json.JSONDecodeError, KeyError):
                self._send(400, {"error": "invalid JSON or missing 'text'"})
                return

            t0 = time.monotonic()
            doc = nlp(text)
            elapsed_ms = (time.monotonic() - t0) * 1000

            entities = [
                {
                    "text": ent.text,
                    "label": ent.label_,
                    "start": ent.start_char,
                    "end": ent.end_char,
                }
                for ent in doc.ents
            ]
            self._send(200, {
                "entities": entities,
                "has_entities": len(entities) > 0,
                "duration_ms": round(elapsed_ms, 2),
            })
        else:
            self._send(404, {"error": "not found"})

    def _send(self, status: int, payload: dict):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)


if __name__ == "__main__":
    server = HTTPServer(("0.0.0.0", PORT), Handler)
    print(f"[ner] server ready on :{PORT}", flush=True)
    server.serve_forever()
