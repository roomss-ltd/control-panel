# Stand-in Hermes for the acceptance suite (no model): records every turn to turns.jsonl, the delivery oracle.
# usage: hermes.py PORT TURNS MODEFILE    MODEFILE holds ok | fail | unknown (re-read on every request)
#   ok      answers like a lead and keeps the turn in readable history
#   fail    refuses the turn with 503 (nothing recorded): delivery must be "failed", retryable
#   unknown records the turn but drops the stream and makes history unreadable: delivery must be "unknown", never resent
import json, sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
LOG, MODE = sys.argv[2], sys.argv[3]; msgs = {}
def mode():
    try: return open(MODE).read().strip() or "ok"
    except OSError: return "ok"
class H(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def j(self, o, code=200):
        b = json.dumps(o).encode(); self.send_response(code); self.send_header("Content-Type", "application/json"); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        p = self.path.split("?")[0]
        if p == "/health/detailed": return self.j({"platforms": {}})
        if mode() == "unknown" and p.startswith("/api/sessions"): return self.j({"error": "history unreadable (stand-in)"}, 500)
        if p == "/api/sessions": return self.j({"data": [{"id": k, "source": "api_server", "message_count": len(v), "last_active": v[-1]["timestamp"]} for k, v in msgs.items()]})
        if p.startswith("/api/sessions/") and p.endswith("/messages"): return self.j({"data": msgs.get(p.split("/")[3], [])})
        self.j({}, 404)
    def do_POST(self):
        n = int(self.headers.get("Content-Length") or 0); body = json.loads(self.rfile.read(n) or b"{}")
        p = self.path.split("?")[0]; m = mode()
        if p == "/api/sessions": return self.j({"ok": True}, 201)
        if p.endswith("/chat/stream"):
            if m == "fail": return self.j({"error": "stand-in refuses"}, 503)
            sid = p.split("/")[3]; t = time.time()
            with open(LOG, "a") as f: f.write(json.dumps({"t": t, "session": sid, "mode": m, "message": body.get("message"), "instructions": body.get("instructions")}) + "\n")
            h = msgs.setdefault(sid, []); h.append({"id": len(h) + 1, "role": "user", "content": body.get("message"), "timestamp": t})
            if m == "unknown":
                self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.end_headers(); return  # stream ends with no completion
            h.append({"id": len(h) + 1, "role": "assistant", "content": "Noted (stand-in lead).", "timestamp": t + 0.01})
            self.send_response(200); self.send_header("Content-Type", "text/event-stream"); self.end_headers()
            self.wfile.write(b'event: assistant.completed\ndata: {"content":"Noted (stand-in lead)."}\n\nevent: run.completed\ndata: {"messages":[1]}\n\n')
            return
        self.j({}, 404)
ThreadingHTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
