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

from hermes_cli.main import main  # noqa: E402  only once the token is out of reach

sys.argv = ["hermes", "serve", *sys.argv[1:]]
sys.exit(main())
