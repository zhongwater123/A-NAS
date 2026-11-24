"""What comes before a montage job's planning, written for the system test (ai/assistant-spike/system-test.sh).

The job's first steps read the photo library's analysis of its videos and allin1's analysis of its song, which need
models the system test does not install. So, with the montage service stopped, this gives the job what those steps
would have kept: an analysis of no videos and a catalog of four cards, each under the input key the job works out for
it, and the song an analysis with a twelve-second excerpt in two parts. The job keeps none of its videos, so that its
steps look up none in the photo library; its song step, its keys from there on and its briefs are the job's own work.

  montage-plan-seed.py STORE JOB VIDEO   STORE is montage.db, JOB the job's id, VIDEO the asset the cards are of

Runs as the montage service's account, which owns the store. The keys follow internal/montage/steps.go (digest, the
analysis and catalog steps' keys, catalogVersion): a change there leaves the job without footage, and the system test
says where the job stopped.
"""
import hashlib
import json
import sqlite3
import sys
from datetime import UTC, datetime


def digest(*parts):
    """internal/montage/steps.go's digest: each part's length in bytes, a colon, the part and a line break."""
    sum_ = hashlib.sha256()
    for part in parts:
        sum_.update(f"{len(part.encode())}:{part}\n".encode())
    return sum_.hexdigest()


def main(store, job, video):
    now = datetime.now(UTC).strftime("%Y-%m-%dT%H:%M:%S.%fZ")
    cards = {}
    for n, (scene, size) in enumerate((("山顶", "全景"), ("步道", "中景"), ("湖边", "远景"), ("营地", "近景")), 1):
        card = f"v1-{n}"
        beats = [{"shot_size": size, "what": f"{scene}的第 {k} 个画面", "frames": [k]} for k in (1, 2)]
        cards[card] = {"id": card, "video": video, "start": 2.0 * (n - 1), "end": 2.0 * n, "times": [2.0 * n - 1.5, 2.0 * n - 0.5],
                       "frames": [f"{card}/1", f"{card}/2"],
                       "answer": {"summary": f"{scene}的一段", "scene": scene, "process": [f"走到{scene}"], "beats": beats}}
    analysis = json.dumps({"videos": None}, separators=(",", ":"))
    analysis_key = digest(analysis)
    catalog = json.dumps({"cards": cards, "catalog": list(cards)}, ensure_ascii=False)
    catalog_key = digest(analysis_key, "catalog/v1")
    music = {"song": "song", "seconds": 12.0, "parts": [
        {"start": 0.0, "end": 6.0, "level": "verse", "slots": [[0.0, 2.0], [2.0, 4.0], [4.0, 6.0]]},
        {"start": 6.0, "end": 12.0, "level": "chorus", "slots": [[6.0, 8.0], [8.0, 10.0], [10.0, 12.0]]}]}
    song = {"structure": {"model": "system-test", "bpm": 120.0, "beats": [0.5 * i for i in range(24)],
                          "downbeats": [2.0 * i for i in range(6)], "segments": []},
            "seconds": 1.0, "selection": {"tempo": 120.0, "bar": 2.0, "excerpt": [0.0, 12.0], "seconds": 12.0, "parts": [], "chorus": True},
            "music": music}
    db = sqlite3.connect(store)
    try:
        with db:
            changed = db.execute("""UPDATE songs SET state = 'ready', reason = '', waiting = '', analysis_version = 'system-test',
                analysis_json = ?, updated_at = ? WHERE id = (SELECT p.song_id FROM jobs j JOIN projects p ON p.id = j.project_id WHERE j.id = ?)""",
                                 (json.dumps(song), now, job)).rowcount
            if changed != 1:
                raise SystemExit(f"the job {job} has no song")
            db.execute("DELETE FROM job_assets WHERE job_id = ?", (job,))
            for name, key, result in (("analysis", analysis_key, analysis), ("catalog", catalog_key, catalog)):
                db.execute("""INSERT INTO steps(job_id, name, input_key, state, result_json, started_at, finished_at) VALUES(?, ?, ?, 'done', ?, ?, ?)
                    ON CONFLICT(job_id, name) DO UPDATE SET input_key = excluded.input_key, state = 'done', result_json = excluded.result_json,
                    finished_at = excluded.finished_at""", (job, name, key, result, now, now))
            changed = db.execute("""UPDATE jobs SET state = 'queued', wait_reason = '', error_class = '', error = '', attempts = 0,
                not_before = '1970-01-01T00:00:00Z', updated_at = ? WHERE id = ? AND state IN ('queued', 'running', 'waiting')""", (now, job)).rowcount
            if changed != 1:
                raise SystemExit(f"the job {job} is not one that runs")
    finally:
        db.close()
    print(f"seeded {job}: analysis {analysis_key[:12]}, catalog {catalog_key[:12]}, {len(cards)} cards")


if __name__ == "__main__":
    main(*sys.argv[1:4])
