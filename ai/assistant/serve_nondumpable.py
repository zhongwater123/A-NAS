"""Runs `hermes serve` with the instance's session token out of the member's reach.

serve reads the token from a file (assistant-start.sh, --ssh-session-token-file) and then holds
it in memory, and any process of the same UID may read /proc/<pid>/mem where ptrace is open to
it (Yama ptrace_scope 0, as on the Experimental NAS); with the terminal tool the member's
assistant runs such processes in the sandbox. A non-dumpable process's /proc files belong to
root, so this marks itself non-dumpable before Hermes runs. The processes Hermes starts are
dumpable again but never had the token. assistant-start.sh runs this with python -P, so the
scripts beside it (gateway.py and others) cannot shadow Hermes's own modules.
"""
import ctypes
import os
import sys

PR_SET_DUMPABLE = 4
libc = ctypes.CDLL(None, use_errno=True)
if libc.prctl(PR_SET_DUMPABLE, 0, 0, 0, 0) != 0:
    sys.exit(f"prctl(PR_SET_DUMPABLE) failed: {os.strerror(ctypes.get_errno())}")

from hermes_cli import gateway_runtime_start  # noqa: E402  only once the token is out of reach
from hermes_cli.gateway_runtime_service import RuntimeStartError  # noqa: E402
from hermes_cli.main import main  # noqa: E402

# The instance runs no gateway. Hermes starts one from any client that needs it and finds none:
# running a scheduled task now (the cron route's trigger, the model's cronjob_manage run) did,
# inside this unit, and the gateway stayed for the unit's life with three MCP servers of its
# own, took over the member's due tasks and recorded its first run as failed (#469). Tasks run
# in a round of the cron unit, which starts and stops its own gateway (cron_round.py), so here
# the start is refused and the caller hears that no gateway serves the home. The name is
# internal to Hermes: an upgrade that moves it stops the instance from starting, loudly.
if not callable(getattr(gateway_runtime_start, "spawn_unmanaged_gateway", None)):
    sys.exit("hermes_cli.gateway_runtime_start.spawn_unmanaged_gateway is gone: see serve_nondumpable.py")


def no_gateway(*_args, **_kwargs):
    raise RuntimeStartError("anas_instance_runs_no_gateway")


gateway_runtime_start.spawn_unmanaged_gateway = no_gateway

sys.argv = ["hermes", "serve", *sys.argv[1:]]
sys.exit(main())
