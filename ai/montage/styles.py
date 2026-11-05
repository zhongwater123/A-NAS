"""How each kind of cut is told (#326): structure, ending and pace, never a picture of one video.

A member picks one, or the planner judges it from the instruction; only that one goes to the
model. Standard library only, for the assistant's MCP server as for the planner (montage.plan).
"""

STYLES = {
    "预告片": "前半段交代人物、地点和处境，按故事线走；后半段挑最有悬念、最有冲击力的画面，越往后越紧；"
              "最后一段用整支片子里最有悬念或最有冲击力的画面收尾；片头片尾字幕、演职员表不用。",
    "旅行": "按时间或路线走；开头用一个能交代地点的全景；中间每处地方挑最有代表性的画面，远景、人和细节穿插；"
            "结尾落在一个安静、有余味的画面上，如日落、远景、离开的背影。",
    "回顾": "按主题或时间分成几组，每组挑最好的几个画面，同一组里由远到近或由静到动；"
            "不必交代来龙去脉；结尾用最有情绪的一个画面。",
}
