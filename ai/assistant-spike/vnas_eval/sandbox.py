"""Where a scenario run's evidence lives on the host, and putting the seed back between runs.

scenarios.py uses this as root in the spike container. Sandbox paths (/nas/...) map to the data
volume as anas-assistant@.service binds them. The seed is restored in place: directories that
exist in both stay, because the units bind some of them (the recycle bins) on their own.
"""
from __future__ import annotations

import glob
import hashlib
import json
import os
import re
import shutil
import subprocess
from pathlib import Path

DATA = Path("/srv/a-nas/data")
# The member's spaces, where a change needs the member's card; /nas/助手 is the assistant's own.
SPACES = ("/nas/个人空间", "/nas/共享空间")


class Layout:
    def __init__(self, user: str, data: Path = DATA):
        self.user, self.data = user, data
        # Longest prefix first: what the member's sandbox sees at each path.
        self.roots = [("/nas/个人空间", data / "spaces" / "private" / user),
                      ("/nas/共享空间", data / "spaces" / "shared"),
                      ("/nas/助手", data / "assistant" / user / "files")]
        self.hermes = data / "assistant" / user / "hermes"

    def host(self, sandbox_path: str) -> Path | None:
        for prefix, root in self.roots:
            if sandbox_path == prefix or sandbox_path.startswith(prefix + "/"):
                return Path(str(root) + sandbox_path[len(prefix):])
        return None

    def sandbox(self, host_path: Path) -> str:
        text = str(host_path)
        for prefix, root in sorted(self.roots, key=lambda r: -len(str(r[1]))):
            if text == str(root) or text.startswith(str(root) + "/"):
                return prefix + text[len(str(root)):]
        raise ValueError(f"{host_path} is outside the member's sandbox")


def skill_files(layout: Layout) -> set[str]:
    """The member's own skills (Hermes writes them under its home)."""
    root = layout.hermes / "skills"
    return {str(p) for p in root.rglob("SKILL.md")} if root.is_dir() else set()


def snapshot(layout: Layout) -> dict[str, str]:
    """What is in the member's spaces: sandbox path -> "dir", "link:<target>" or the content's sha256."""
    out = {}
    for space in SPACES:
        root = layout.host(space)
        if root is None or not root.is_dir():
            continue
        for folder, dirs, files in os.walk(root):
            for name in dirs + files:
                p = Path(folder) / name
                key = layout.sandbox(p)
                if p.is_symlink():
                    out[key] = "link:" + os.readlink(p)
                elif p.is_dir():
                    out[key] = "dir"
                else:
                    out[key] = hashlib.sha256(p.read_bytes()).hexdigest()
    return out


def jobs_file(layout: Layout) -> Path:
    return layout.hermes / "cron" / "jobs.json"


def read_jobs(layout: Layout) -> list[dict]:
    try:
        return json.loads(jobs_file(layout).read_text(encoding="utf-8")).get("jobs") or []
    except (OSError, ValueError):
        return []


PROMPT_FRAME = re.compile(r"(?m)^\*\*Prompt Characters:\*\* (\d+)\n## Prompt\n\n")
RESPONSE_FRAME = re.compile(r"\*\*Response Characters:\*\* (\d+)\n## Response\n\n")


def answer_of(archive: str) -> str:
    """The task's answer from the run's record (cron/output/<job>/<time>.md), without the prompt
    it was given, which holds the member's own words. Read by the length frames Hermes writes
    around both (cron/scheduler_prompt.py _archive_answer), else after the last "## Response"."""
    prompt = PROMPT_FRAME.search(archive)
    if prompt:
        response = RESPONSE_FRAME.match(archive, prompt.end() + int(prompt.group(1)) + 2)
        if response:
            return archive[response.end():response.end() + int(response.group(1))].strip()
    return archive.rpartition("## Response")[2].strip() if "## Response" in archive else archive.strip()


def run_jobs(layout: Layout, run=subprocess.run, requests: Path = Path("/run/a-nas/assistant-runs")) -> list[dict]:
    """Run each scheduled task once, as A-NAS runs a task the member asked to run now (#469):
    a request per task in /run/a-nas/assistant-runs/<member> (run-now.sh), then one round of the
    cron unit (cron_round.py), with no one there to answer. Each task's run: {job, status, error,
    output}, "not run" for a task the round did not take up."""
    jobs = read_jobs(layout)
    if not jobs:
        return []
    waiting = requests / layout.user
    waiting.mkdir(mode=0o700, parents=True, exist_ok=True)
    before = {j["id"]: j.get("last_run_at") for j in jobs}
    for j in jobs:
        (waiting / j["id"]).touch()
    run(["/usr/bin/systemctl", "start", f"anas-assistant-cron@{layout.user}.service"], check=False, timeout=1800)
    runs = []
    for j in read_jobs(layout):
        if j["id"] not in before or j.get("last_run_at") == before[j["id"]]:
            runs.append({"job": j.get("name") or j["id"], "status": "not run", "error": j.get("last_error"), "output": ""})
            continue
        outputs = sorted((layout.hermes / "cron" / "output" / j["id"]).glob("*.md"))
        text = answer_of(outputs[-1].read_text(encoding="utf-8", errors="replace")) if outputs else ""
        runs.append({"job": j.get("name") or j["id"], "status": j.get("last_status"), "error": j.get("last_error"),
                     "output": text})
    return runs


class HostWorld:
    """vnas_eval.asserts.World on the data volume, after a conversation."""

    def __init__(self, layout: Layout, skills_before: set[str], files_before: dict[str, str] | None = None,
                 runs: list[dict] | None = None):
        self.layout, self.skills_before = layout, skills_before
        self.files_before, self.runs = files_before, runs or []

    def changed(self) -> list[str]:
        if self.files_before is None:
            raise ValueError("no snapshot of the spaces from before the conversation")
        now = snapshot(self.layout)
        return sorted(p for p in set(now) | set(self.files_before) if now.get(p) != self.files_before.get(p))

    def jobs(self) -> list[dict]:
        return [{k: j.get(k) for k in ("id", "name", "prompt", "schedule")} for j in read_jobs(self.layout)]

    def job_runs(self) -> list[dict]:
        return self.runs

    def glob(self, pattern: str) -> list[str]:
        host = self.layout.host(pattern)
        if host is None:
            raise ValueError(f"{pattern} is not a sandbox path (/nas/个人空间, /nas/共享空间 or /nas/助手)")
        # The data volume's own path has no wildcards; the pattern's are the member's part.
        return sorted(self.layout.sandbox(Path(p)) for p in glob.glob(str(host)))

    def read(self, path: str) -> str | None:
        host = self.layout.host(path)
        return host.read_text(encoding="utf-8", errors="replace") if host and host.is_file() else None

    def memory(self) -> str:
        folder = self.layout.hermes / "memories"
        return "\n".join(p.read_text(encoding="utf-8", errors="replace") for p in sorted(folder.glob("*.md"))) \
            if folder.is_dir() else ""

    def skills_created(self) -> int:
        return len(skill_files(self.layout) - self.skills_before)


def plant(layout: Layout, files, run=subprocess.run) -> None:
    """Write a case's files (vnas_eval.asserts.PlantedFile) as the members who wrote them, so their
    own permissions decide where a file can go; the next restore removes them with the rest."""
    for f in files:
        host = layout.host(f.path)
        if host is None:
            raise ValueError(f"{f.path} is not a sandbox path")
        run(["/usr/bin/setpriv", f"--reuid={f.by}", f"--regid={f.by}", "--init-groups", "--",
             "/usr/bin/sh", "-c", 'mkdir -p "$(dirname "$1")" && cat > "$1"', "sh", str(host)],
            input=f.content.encode("utf-8"), check=True)


# ---- the seed ----

def _remove(path: Path) -> None:
    if path.is_dir() and not path.is_symlink():
        shutil.rmtree(path)
    elif path.exists() or path.is_symlink():
        path.unlink()


def _copy(src: Path, dst: Path) -> None:
    """cp -a keeps owner, mode, times, ACLs and extended attributes."""
    subprocess.run(["/usr/bin/cp", "-a", "--no-target-directory", str(src), str(dst)], check=True)


def _same(a: Path, b: Path) -> bool:
    if not (b.exists() or b.is_symlink()):
        return False
    sa, sb = a.lstat(), b.lstat()
    if (sa.st_mode, sa.st_uid, sa.st_gid) != (sb.st_mode, sb.st_uid, sb.st_gid):
        return False
    if a.is_symlink():
        return os.readlink(a) == os.readlink(b)
    return (sa.st_size, sa.st_mtime_ns) == (sb.st_size, sb.st_mtime_ns)


def restore_tree(src: Path, dst: Path) -> None:
    """Make dst hold what src holds, keeping every directory present in both."""
    for entry in list(dst.iterdir()):
        twin = src / entry.name
        if not (twin.exists() or twin.is_symlink()):
            _remove(entry)
    for entry in src.iterdir():
        target = dst / entry.name
        if entry.is_dir() and not entry.is_symlink():
            if target.is_dir() and not target.is_symlink():
                restore_tree(entry, target)
                continue
            _remove(target)
            _copy(entry, target)
        elif not _same(entry, target):
            _remove(target)
            _copy(entry, target)


class Seed:
    """The spaces and the member's Hermes state as they were before the first run: memory,
    skills, scheduled tasks and past conversations (sessions/ and state.db, which session_search
    reads), so that no run finds what an earlier one said, scheduled or remembered.

    The skills folder goes back whole, .bundled_manifest included: Hermes records each bundled
    skill it copied there, and one missing from the folder but in the manifest counts as deleted
    by the member and is never copied again (tools/skills_sync.py). Removing only what a run
    added would therefore lose the bundled skills for every later run."""

    DB = ("state.db", "state.db-wal", "state.db-shm")

    def __init__(self, layout: Layout, store: Path):
        self.layout, self.store = layout, store
        self.trees = [(layout.data / "spaces", store / "spaces")] + [
            (layout.hermes / name, store / name) for name in ("memories", "skills", "cron", "sessions")]
        self.files = [(layout.hermes / name, store / "hermes-files" / name) for name in self.DB]

    def take(self) -> None:
        _remove(self.store)
        self.store.mkdir(parents=True, mode=0o700)
        (self.store / "hermes-files").mkdir(mode=0o700)
        for live, copy in self.trees + self.files:
            if live.exists():
                _copy(live, copy)

    def restore(self) -> None:
        """With the member's instance stopped: the seed back, and the member's Hermes state as it was."""
        for live, copy in self.trees:
            if copy.is_dir():
                live.mkdir(parents=True, exist_ok=True)
                restore_tree(copy, live)
            else:
                _remove(live)
        for live, copy in self.files:
            _remove(live)
            if copy.exists():
                _copy(copy, live)
