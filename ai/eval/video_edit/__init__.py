"""The video edit plans' evaluation harness (docs/specs/video-edit-evaluation.md).

The host side (__main__) needs only the standard library and drives Docker;
inner runs inside the evaluation image, which adds PyYAML, jsonschema and
OpenTimelineIO. sampler runs inside every plan's container on the image's
system Python.
"""

# 口径: change it with any change to what is measured or how, and measure both
# plans again.
METHOD_VERSION = "v1"
IMAGE = "anas-video-edit-eval:v1"

# The host constraint profile (docs/specs/video-edit-evaluation.md#宿主机约束配置).
CPUSET = "0-7"
CPUS_NEEDED = 8
MEMORY_LIMIT = "3.4g"
TIMEOUT_SECONDS = 3 * 3600

BASELINE = "baseline"
CASE_PATTERN = r"^case-\d{2}$"
OUTPUTS = ("timeline.otio", "output.mp4", "trace.json")

CATEGORIES = {
    "decode": "读取与解码",
    "segment": "切分",
    "understand": "理解",
    "arrange": "选择与编排",
    "refine": "精修",
    "render": "渲染",
    "check": "检查",
    "other": "其他",
}
RUNS_ON = {
    "nas-cpu": "宿主机 CPU",
    "nas-igpu": "宿主机核显",
    "lan-gpu": "局域网 GPU",
    "cloud": "云端",
}
EXTERNAL = ("lan-gpu", "cloud")
