"""ATC-309 provider probe: what Codex's agent sees when a multi-question
request_user_input is answered the way ATC's T3 translation answers it —
a conversational reply keyed to the first question only, or a structured
partial answer for one question only. Drives `codex app-server` over
stdio in plan mode (request_user_input is only available there), on an
ephemeral thread in an empty temp directory, read-only sandbox, no
approvals. Prints the questions asked, the answer map sent, and the
agent's final message for each case."""
import json, os, subprocess, sys, tempfile, threading, queue, time

MODEL = "gpt-5.6-sol"
CASES = {
    "reply": lambda qs: {qs[0]["id"]: {"answers": ["Blue is fine. Actually, forget the tooling question - and please skip the tests entirely; instead tell me in one sentence what you would do first."]}},
    "partial": lambda qs: {qs[1]["id"]: {"answers": ["make"]}},
}
case = sys.argv[1]
os.makedirs("/tmp/atc-309", exist_ok=True)
cwd = tempfile.mkdtemp(prefix="atc-309-probe-")
proc = subprocess.Popen(["codex", "app-server"], stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=open(f"/tmp/atc-309/{case}.stderr", "w"), text=True, cwd=cwd, bufsize=1)
lines = queue.Queue()
def reader():
    for line in proc.stdout:
        lines.put(line)
    lines.put(None)
threading.Thread(target=reader, daemon=True).start()
log = open(f"/tmp/atc-309/{case}.jsonl", "w")
next_id = [0]
def send(msg):
    log.write("-> " + json.dumps(msg) + "\n"); log.flush()
    proc.stdin.write(json.dumps(msg) + "\n"); proc.stdin.flush()
def request(method, params):
    next_id[0] += 1
    send({"id": next_id[0], "method": method, "params": params})
    return next_id[0]
def recv(timeout=300):
    line = lines.get(timeout=timeout)
    if line is None:
        raise SystemExit("app-server exited")
    log.write("<- " + line); log.flush()
    return json.loads(line)
def wait_response(rid):
    while True:
        m = recv()
        if m.get("id") == rid and ("result" in m or "error" in m):
            if "error" in m:
                raise SystemExit(f"{m['error']}")
            return m["result"]
        handle(m)
questions = []
sent_answers = {}
final = []
def handle(m):
    method = m.get("method")
    if method == "item/tool/requestUserInput" and "id" in m:
        qs = m["params"]["questions"]
        questions.extend(qs)
        answers = CASES[case](qs)
        sent_answers.update(answers)
        send({"id": m["id"], "result": {"answers": answers}})
    elif method == "item/completed":
        item = m["params"]["item"]
        if item.get("type") == "agentMessage":
            final.append(item.get("text", ""))
    elif method and method.endswith("requestApproval") and "id" in m:
        send({"id": m["id"], "result": {"decision": "decline"}})
rid = request("initialize", {"clientInfo": {"name": "atc-309-probe", "version": "0"}, "capabilities": {"experimentalApi": True}})
wait_response(rid)
send({"method": "initialized"})
rid = request("thread/start", {"cwd": cwd, "ephemeral": True, "model": MODEL, "approvalPolicy": "never", "sandbox": "read-only"})
thread = wait_response(rid)["thread"]
prompt = ("Do not run any tools or read any files. First, call the request_user_input tool exactly once with exactly three questions, in this order: "
          "(1) id color, header Color, question 'Which color should the badge be?', options Red and Blue; "
          "(2) id tool, header Tool, question 'Which build tool?', options go and make; "
          "(3) id tests, header Tests, question 'Run the test suite afterwards?', options Yes and No. "
          "After you receive the response, do not ask anything else. Reply with exactly three lines: "
          "line 1: 'ANSWERED: ' followed by the ids of the questions that were answered, comma-separated, or 'none'; "
          "line 2: 'TEXT: ' followed by the verbatim answer text(s) you received; "
          "line 3: 'PLAN: ' followed by one sentence saying what you would do next, honoring any change of direction in the answer.")
rid = request("turn/start", {"threadId": thread["id"], "input": [{"type": "text", "text": prompt}],
    "collaborationMode": {"mode": "plan", "settings": {"model": MODEL, "reasoning_effort": "low", "developer_instructions": None}},
    "approvalPolicy": "never"})
turn = wait_response(rid)["turn"]
deadline = time.time() + 400
while time.time() < deadline:
    m = recv(timeout=deadline - time.time())
    if m.get("method") == "turn/completed":
        break
    handle(m)
print(json.dumps({"case": case, "questions": [{"id": q["id"], "options": [o["label"] for o in (q.get("options") or [])], "isOther": q.get("isOther")} for q in questions],
                  "answers_sent": sent_answers, "agent_final": final}, indent=2))
proc.stdin.close(); proc.terminate()
