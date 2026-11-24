"""CPU scorer child. Its native runtime and IPC resources live only for this scoring phase."""

import json
import sys
from multiprocessing.connection import Connection
from pathlib import Path


def serve(connection, bundle, threads):
    try:
        from qv_head.release import ReleaseManifest

        manifest = ReleaseManifest.model_validate_json(connection.recv_bytes())
        import torch

        torch.set_num_threads(threads)
        from qv_head.scorer import Scorer, ScorerError

        scorer = Scorer(bundle, manifest, device="cpu")
    except Exception as error:  # noqa: BLE001  contain initialization failures in this optional provider
        connection.send_bytes(json.dumps(["unavailable", str(error)]).encode())
        return
    connection.send_bytes(b'["ready", null]')
    while True:
        try:
            image = connection.recv_bytes()
        except EOFError:
            return
        try:
            result = scorer.score_bytes(image)
            reply = ["scores", {"aesthetic": round(result.aesthetic, 4), "technical": round(result.technical, 4)}]
        except ScorerError as error:
            reply = ["invalid", str(error)]
        connection.send_bytes(json.dumps(reply).encode())


if __name__ == "__main__":
    with Connection(int(sys.argv[1])) as channel:
        serve(channel, Path(sys.argv[2]), int(sys.argv[3]))
