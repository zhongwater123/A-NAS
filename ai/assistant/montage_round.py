"""The montage center's planning in a round of the member's cron unit (docs/architecture/montage-center.md, 发起与结束).

cron_round.py calls this around its own round:
- clean, always and before the gateway starts: a planning job never outlives its round, so every job named
  "剪辑规划 <round>" still in Hermes was left by a round that was killed, and a gateway would run it again as soon as
  it is ready. They are removed with their runs' sessions, whether the montage center still knows their rounds or
  not (a round goes with its project or account), and the rounds it lists as never reported are reported cleaned.
- begin, only when the waker left this round a planning token (montage-plan, which run-requests.sh moved here) and
  the gateway is this round's own: claim a round, leave the waker a receipt of the claim, check that the model
  answers, create the job paused with only the montage toolset, record that it started, and make it due. The
  gateway's tick hands it over with the jobs that are due.
- finish, once the round's runs are over: remove the job's run sessions and the job, then report how it ended.

None of the three lets a failure of its own out: the round runs the member's own due tasks too, which the montage
center must not stop.

The prompt is the fixed template the package installs (MONTAGE_PROMPTS/<template>.md); only the round's ids, its
token and the target seconds are filled in, each checked first, so the montage center cannot write the prompt.

The receipt is how the waker counts a planning grant's rounds (cron-waker.py): the token's nonce, written into the
member's Hermes home once a round is claimed. The montage center cannot write there, so it cannot have a round run
uncounted; a round that claimed nothing leaves none, and the waker gives the grant's round back.

Hermes's job table and session store are internal to Hermes, as cron_round.py's other calls: an upgrade that moves
them fails the round loudly (docs/research/hermes-agent-runtime.md, "以后怎么升级").
"""

import contextlib
import http.client
import json
import os
import re
import string
import urllib.request

from cron.jobs import create_job, load_jobs, remove_job, trigger_job
from hermes_constants import get_hermes_home
from hermes_state import SessionDB

from anas_window_call import UnixHTTP

SOCKET = "/run/anas-assistant-montage/montage.sock"
PROMPTS = os.environ.get("ANAS_MONTAGE_PROMPTS", "/usr/lib/a-nas/assistant/montage-prompts")
MODELS = "http://127.0.0.1:18001/v1/models"  # the model bridge of the member's namespace
PLAN_FILE = "montage-plan"  # the waker's token, as run-requests.sh names it in this round's directory
RECEIPT = "anas-montage-claim"  # in the member's Hermes home, where the waker reads it
NAME = "剪辑规划 "
# The montage center's round and job ids are a prefix and crypto/rand.Text (26 base32 letters), its tokens
# 16 random bytes in hex (internal/montage); internal/montage/roundscript_test.go holds them to these.
ROUND_ID = re.compile(r"round:[A-Z2-7]{26}")
JOB_ID = re.compile(r"job:[A-Z2-7]{26}")
TOKEN = re.compile(r"[0-9a-f]{32}")
NONCE = re.compile(r"[0-9a-f]{32}")
TIMEOUT = 30
# Only the montage toolset. no_mcp keeps Hermes from adding every enabled MCP server when the configuration it
# loaded has no server named montage: a job's list that names no known server gets them all (anas, companion).
TOOLSETS = ["montage", "no_mcp"]


def say(text):
    print(f"montage round: {text}", flush=True)


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
    """The planning jobs that could not be removed must not run in this round's gateway."""
    from cron.jobs import pause_job

    for job in planning_jobs().values():
        try:
            pause_job(job["id"], reason="剪辑规划的遗留任务，等下一轮清理")
        except TypeError:  # a Hermes whose pause_job takes no reason
            pause_job(job["id"])


def clean():
    """Remove what earlier rounds left, before the gateway starts. Whatever fails here, the round goes on."""
    try:
        left = sweep()
    except Exception as broken:  # noqa: BLE001 - the member's own tasks run whatever becomes of this.
        say(f"removing the planning jobs left over failed ({type(broken).__name__}: {broken}); pausing them")
        left = None
    if left is None or left:
        try:
            pause_all()
        except Exception as broken:  # noqa: BLE001
            say(f"pausing them failed too ({type(broken).__name__}: {broken})")
    if left is None or not os.path.exists(SOCKET):
        return
    try:
        report(left)
    except Exception as broken:  # noqa: BLE001
        say(f"the rounds cleaned up were not reported ({type(broken).__name__}: {broken}); the next round reports them")


def sweep():
    """Remove every planning job; the rounds whose job could not be removed."""
    left = set()
    for round_id, job in planning_jobs().items():
        try:
            remove(job["id"])
            say(f"removed the job of round {round_id}")
        except Exception as broken:  # noqa: BLE001 - one job that will not go does not keep the others.
            left.add(round_id)
            say(f"the job of round {round_id} could not be removed ({type(broken).__name__}: {broken})")
    return left


def report(left):
    """Tell the montage center which rounds it lists as never reported have nothing left in Hermes."""
    status, answer = ask("/v1/rounds/stale")
    if status != 200:
        say(f"stale answered {status}")
        return
    cleaned = [stale.get("roundId", "") for stale in answer.get("rounds", []) if isinstance(stale, dict)]
    cleaned = [round_id for round_id in cleaned if ROUND_ID.fullmatch(round_id) and round_id not in left]
    if cleaned:
        # The montage center takes at most 100 at a time; the next round reports the rest.
        ask("/v1/rounds/cleaned", {"roundIds": cleaned[:100]})


def nonce(runs):
    """The nonce of the waker's planning token in this round's directory, or None without one."""
    if not runs:
        return None
    try:
        with open(os.path.join(runs, PLAN_FILE), encoding="ascii") as token:
            value = token.read(65).strip()
    except (OSError, ValueError):
        return None
    return value if NONCE.fullmatch(value) else None


def wanted(runs):
    return nonce(runs) is not None


def acknowledge(value, round_id):
    """Leave the waker the receipt of a claim: this token's round was claimed, and counts."""
    path = os.path.join(str(get_hermes_home()), RECEIPT)
    partial = f"{path}.{os.getpid()}"
    with open(partial, "w", encoding="utf-8") as out:
        json.dump({"nonce": value, "round": round_id}, out)
    os.replace(partial, path)


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
    """Claim a round and hand its job to Hermes; the round's ids, or None. Whatever fails, the round goes on."""
    value = nonce(runs)
    if value is None or not os.path.exists(SOCKET):
        return None
    try:
        status, claim = ask("/v1/rounds/claim")
    except Unreachable as broken:
        say(f"the montage center did not answer the claim: {broken}")
        return None
    if status != 200:
        say(f"no round to plan ({status})")
        return None
    round_id = claim.get("roundId", "") if isinstance(claim, dict) else ""
    if not ROUND_ID.fullmatch(round_id):
        say("the claim named no round")
        return None
    try:
        return hand_over(value, round_id, claim)
    except Exception as broken:  # noqa: BLE001 - the member's own tasks run whatever becomes of this.
        abandon(round_id, f"这一轮没能交给 VNAS：{type(broken).__name__}: {broken}"[:400])
        return None


def hand_over(value, round_id, claim):
    """The claimed round's job in Hermes, due; None once the round is abandoned or refused."""
    # Before anything can call the model: a round the waker cannot count is not planned.
    acknowledge(value, round_id)
    try:
        text = prompt(claim)
    except (OSError, ValueError, KeyError) as broken:
        abandon(round_id, f"the claim cannot be planned here: {broken}")
        return None
    if not model_answers():
        abandon(round_id, "VNAS 的模型连不上：设备上没有密钥，或模型网关没有回应")
        return None
    job = create_job(prompt=text, schedule="every 1440m", name=NAME + round_id, enabled_toolsets=list(TOOLSETS),
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
    """Remove the round's job and its sessions, then report how it ended. Whatever fails, the round goes on."""
    if planned is None:
        return
    round_id, job_id = planned
    outcome = "unknown"
    try:
        job = next((j for j in load_jobs() if j.get("id") == job_id), None) or {}
        outcome = str(job.get("last_status") or ("never ran" if not job.get("last_run_at") else "unknown"))
        if job.get("last_error"):
            outcome += ": " + str(job["last_error"])[:400]
        remove(job_id)
    except Exception as broken:  # noqa: BLE001 - the end is reported all the same; the next round removes the job.
        say(f"the job of round {round_id} could not be removed ({type(broken).__name__}: {broken})")
    try:
        ask(f"/v1/rounds/{round_id}/end", {"outcome": outcome})
    except Unreachable as broken:
        say(f"the end of round {round_id} was not reported ({broken}); the next round reports it")
    say(f"round {round_id} ended: {outcome}")
