"""一次剪辑的选项与一组素材的设定，取代第六版散落的 CUT_* 环境变量。"""

from dataclasses import dataclass, field


@dataclass(frozen=True)
class Case:
    """一组素材：键（cards-<key>.json）、视频索引名、剪辑要求、体裁、成片文件名里的名字。"""

    key: str
    index: str
    instruction: str
    style: str
    name: str
    target: float = 90.0


@dataclass(frozen=True)
class CutOptions:
    """tag：这一次剪辑的文件名后缀（第六版的 CUT_TAG）；free_order：不要求拍摄时间顺序（CUT_ORDER=free）；
    labels：人标的节拍精彩度（CUT_LABELS 读进来的 {节拍: "3"|"2"|"1"|"x"}）；labels_file：它的文件名，记进结果；
    wide：只用横拍（CUT_WIDE=1）；quality：按画质过滤（CUT_QUALITY）；ask：用户在网页上补充的剪辑要求，接在
    这组素材的要求后面；videos：只用这些视频（网页上勾选的），None 是全部。"""

    tag: str = ""
    free_order: bool = False
    labels: dict = field(default_factory=dict)
    labels_file: str = ""
    wide: bool = False
    quality: bool = True
    ask: str = ""
    videos: frozenset | None = None

    def instruction(self, case: Case) -> str:
        base = case.instruction.replace("按拍摄时间顺序", "") if self.free_order else case.instruction
        return f"{base}；{self.ask.strip()}" if self.ask.strip() else base
