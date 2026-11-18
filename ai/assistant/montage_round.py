"""The montage center's planning in a round of the member's cron unit (docs/architecture/montage-center.md, 发起与结束).

cron_round.py calls this around its own round:
- clean, always and before the gateway starts: the rounds the montage center lists as claimed and never reported
  (stale) left a job named "剪辑规划 <round>" and its run's sessions in Hermes, which a gateway would run again as
  soon as it is ready; they are removed and reported. Unable to ask, every such job is paused instead.
- begin, only when the waker left this round a planning token (montage-plan, which run-requests.sh moved here) and
  the gateway is this round's own: claim a round, check that the model answers, create the job paused with only
  the montage toolset, record that it started, and make it due. The gateway's tick hands it over with the jobs that
  are due.
- finish, once the round's runs are over: remove the job's run sessions and the job, then report how it ended.

The prompt is the fixed template the package installs (MONTAGE_PROMPTS/<template>.md); only the round's ids, its
token and the target seconds are filled in, each checked first, so the montage center cannot write the prompt.

Hermes's job table and session store are internal to Hermes, as cron_round.py's other calls: an upgrade that moves
them fails the round loudly (docs/research/hermes-agent-runtime.md, "以后怎么升级").
"""

import contextlib
import http.client
import json
import os
import re
import socket
import string
import urllib.request

from cron.jobs import create_job, load_jobs, remove_job, trigger_job
from hermes_state import SessionDB

SOCKET = "/run/anas-assistant-montage/montage.sock"
PROMPTS = os.environ.get("ANAS_MONTAGE_PROMPTS", "/usr/lib/a-nas/assistant/montage-prompts")
MODELS = "http://127.0.0.1:18001/v1/models"  # the model bridge of the member's namespace
PLAN_FILE = "montage-plan"  # the waker's token, as run-requests.sh names it in this round's directory
NAME = "剪辑规划 "
ROUND_ID = re.compile(r"round:[A-Z2-7]{24}")
JOB_ID = re.compile(r"job:[A-Z2-7]{24}")
TOKEN = re.compile(r"[0-9a-f]{32}")
TIMEOUT = 30


def say(text):
    print(f"montage round: {text}", flush=True)


class UnixHTTP(http.client.HTTPConnection):
    def __init__(self, path, timeout):
        super().__init__("localhost")
        self.path = path
        self.wait = timeout

    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX)
        self.sock.settimeout(self.wait)
        self.sock.connect(self.path)


class Unreachable(Exception):
    """The montage center did not answer."""


def ask(route, body=None):
    """(status, answer) of one call on the montage center's assistant socket."""
    conn = UnixHTTP(SOCKET, TIMEOUT)
    try:
        conn.request("POST", route, json.dumps(body or {}).encode(), {"Content-Type": "application/json"})
        response = conn.getresponse()
        raw = response.read(1 << 20)
    except (OSError, http.client.HTTPException) as broken:
        raise Unreachable(str(broken)) from None
    finally:
        conn.close()
    try:
        return response.status, json.loads(raw) if raw else {}
    except ValueError:
        return response.status, {}


def planning_jobs():
    """Hermes's jobs that this module created, by round."""
    return {job["name"][len(NAME):]: job for job in load_jobs()
            if isinstance(job.get("name"), str) and job["name"].startswith(NAME) and ROUND_ID.fullmatch(job["name"][len(NAME):])}


def remove(job_id):
    """The job's run sessions, then the job (Hermes probe, conclusion 4)."""
    db = SessionDB()
    for run in db.list_cron_job_runs(job_id, limit=100, offset=0):
        session = run.get("id") or run.get("session_id")
        if session:
            db.delete_session(str(session))
    remove_job(job_id)


def pause_all():
    """Without the montage center, the planning jobs a killed round left must not run in this round's gateway."""
    from cron.jobs import pause_job

    for job in planning_jobs().values():
        try:
            pause_job(job["id"], reason="剪辑中心连不上，等下一轮清理")
        except TypeError:  # a Hermes whose pause_job takes no reason
            pause_job(job["id"])


def clean():
    """Remove what earlier rounds left, before the gateway starts."""
    if not os.path.exists(SOCKET):
        return
    try:
        status, answer = ask("/v1/rounds/stale")
    except Unreachable as broken:
        say(f"the montage center did not answer ({broken}); pausing the planning jobs left over")
        pause_all()
        return
    if status != 200:
        say(f"stale answered {status}; pausing the planning jobs left over")
        pause_all()
        return
    jobs = planning_jobs()
    cleaned = []
    for stale in answer.get("rounds", []):
        round_id = stale.get("roundId", "")
        if not ROUND_ID.fullmatch(round_id):
            continue
        if round_id in jobs:
            remove(jobs[round_id]["id"])
            say(f"removed the job of round {round_id}")
        cleaned.append(round_id)
    if cleaned:
        ask("/v1/rounds/cleaned", {"roundIds": cleaned})


def wanted(runs):
    return bool(runs) and os.path.isfile(os.path.join(runs, PLAN_FILE))


def model_answers():
    """Whether the model gateway answers, without calling the model or spending tokens."""
    try:
        with urllib.request.urlopen(MODELS, timeout=10) as response:
            return response.status == 200
    except OSError:
        return False


def prompt(claim):
    """The template filled with the round's checked ids, token and target seconds."""
    template = claim.get("template", "")
    if not re.fullmatch(r"plan-v[0-9]+", template):
        raise ValueError(f"template {template!r}")
    for key, pattern in (("roundId", ROUND_ID), ("jobId", JOB_ID), ("attemptToken", TOKEN)):
        if not isinstance(claim.get(key), str) or not pattern.fullmatch(claim[key]):
            raise ValueError(f"{key} is not what a round's is")
    target = claim.get("targetSeconds")
    if not isinstance(target, int) or isinstance(target, bool) or not 1 <= target <= 3600:
        raise ValueError("targetSeconds is not a whole number of seconds")
    with open(os.path.join(PROMPTS, template + ".md"), encoding="utf-8") as source:
        text = source.read()
    return string.Template(text).substitute(round_id=claim["roundId"], job_id=claim["jobId"],
                                            token=claim["attemptToken"], target=target)


def abandon(round_id, reason):
    say(f"abandoning round {round_id}: {reason}")
    # Unreachable, the montage center abandons it itself five minutes after its claim.
    with contextlib.suppress(Unreachable):
        ask(f"/v1/rounds/{round_id}/abandon", {"outcome": reason})


def begin(runs):
    """Claim a round and hand its job to Hermes; the round's ids, or None."""
    if not wanted(runs) or not os.path.exists(SOCKET):
        return None
    try:
        status, claim = ask("/v1/rounds/claim")
    except Unreachable as broken:
        say(f"the montage center did not answer the claim: {broken}")
        return None
    if status != 200:
        say(f"no round to plan ({status})")
        return None
    round_id = claim.get("roundId", "")
    if not ROUND_ID.fullmatch(round_id):
        say("the claim named no round")
        return None
    try:
        text = prompt(claim)
    except (OSError, ValueError, KeyError) as broken:
        abandon(round_id, f"the claim cannot be planned here: {broken}")
        return None
    if not model_answers():
        abandon(round_id, "VNAS 的模型连不上：设备上没有密钥，或模型网关没有回应")
        return None
    job = create_job(prompt=text, schedule="every 1440m", name=NAME + round_id, enabled_toolsets=["montage"],
                     paused=True, paused_reason="剪辑规划，等回合脚本触发")
    try:
        status, _ = ask(f"/v1/rounds/{round_id}/started", {"runRef": job["id"]})
    except Unreachable:
        status = 0
    if status != 204:
        # Abandoned or over meanwhile, or not recorded: no model call without a recorded start.
        remove(job["id"])
        say(f"round {round_id} was not recorded as started ({status}); its job is removed")
        return None
    trigger_job(job["id"])
    say(f"round {round_id} handed to Hermes as job {job['id']}")
    return round_id, job["id"]


def finish(planned):
    """Remove the round's job and its sessions, then report how it ended."""
    if planned is None:
        return
    round_id, job_id = planned
    job = next((j for j in load_jobs() if j.get("id") == job_id), None) or {}
    outcome = str(job.get("last_status") or "unknown")
    if job.get("last_error"):
        outcome += ": " + str(job["last_error"])[:400]
    remove(job_id)
    try:
        ask(f"/v1/rounds/{round_id}/end", {"outcome": outcome})
    except Unreachable as broken:
        say(f"the end of round {round_id} was not reported ({broken}); the next round reports it")
    say(f"round {round_id} ended: {outcome}")
