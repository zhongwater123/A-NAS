"""两步规划和卡片描述用的提示词，作为数据（第六版 cut.py、tag_beats.py 原文）。

改提示词只改这里；拼装请求的逻辑在 plan.py 和 describe.py。
"""

# 不要求拍摄时间顺序时（CutOptions.free_order），要求里和第一步去掉的说法，以及加上的“同一地点连续”。
ORDER_ASKS = (("开头、中间、结尾按拍摄时间先后安排；", ""), ("检查内容和先后顺序", "检查内容"),
              ("，按拍摄时间先后排列，每部分里的画面也按拍摄时间先后排列：", "："),
              # 2026-11-11 (the user's wording, its example too): free order cut 新西兰 back and forth between
              # places, its helicopter flight broken by roads, sheep and lakes.
              ("- 每部分的画面个数不超过", "- 同一地点的画面连续排列：切到别的地点后，不再切回之前用过的地点。比如开头是雪山，"
               "中间切到别的地方，之后又切回雪山，画面就不连贯了；\n- 每部分的画面个数不超过"))

# 人标的节拍精彩度（CutOptions.labels）：第一步看每张卡片最好的一档，第二步看每个节拍的。
CARD_MARKS = ("每行：卡片 id {where}起始时间 时长 | 场景 | 过程（这张卡片里依次发生的事）",
              "每行：卡片 id {where}起始时间 时长 | 场景 | 过程（这张卡片里依次发生的事） | 精彩度\n"
              "精彩度是人看过画面后标的，按卡片里最好的节拍算：精彩是有值得放在开场或高潮段的画面（括号里是精彩节拍的个数），"
              "可用是好看但不突出，平淡是交代、过渡或普通记录，有问题是画面有遮挡、模糊、抖动，不能用。")
PICK_MARKS = ("- 不要用片头片尾字幕、黑场或纯文字画面。",
              "- 不要用片头片尾字幕、黑场或纯文字画面；\n"
              "- 精彩度：开场和高潮段优先用标为精彩的卡片，精彩的卡片优先选用；衔接、交代和过渡照样可以用平淡的卡片；不要用有问题的卡片。")
SHOTS_MARKS = ("- 这几张卡片里如果有更合适、更精彩的节拍，按需挑选，这一条优先级其次；",
               "- 这几张卡片里如果有更合适、更精彩的节拍，按需挑选；节拍后面【】里是人看过画面后标的精彩度（精彩、可用、平淡、有问题）："
               "符合画面要求的节拍里优先挑精彩的，其次可用；不要挑有问题的。这一条优先级其次；")

# 第一步：方案 B 的大纲请求，用我们的卡片；衔接规则在前；这类素材常用的结构作参考；音乐段落。
PICK_SYSTEM = "你是剪辑规划，按用户的剪辑要求写一份成片大纲。画面由程序按大纲剪出，配上给定的音乐，不用原声，不加字幕。"
PICK = """用户的剪辑要求：
「{instruction}」

下面是 {total_min:.1f} 分钟原始素材切成的 {n} 张卡片，按拍摄时间排列。卡片按画面变化切分，一张卡片是同一处、同一件事的一段连续画面；过程由本机小模型看关键帧写成，可能看错。报幕、片头片尾字幕、黑场和纯文字画面已经去掉了。
每行：卡片 id {where}起始时间 时长 | 场景 | 过程（这张卡片里依次发生的事）

{catalog}
{music}{structure}
衔接（规划时照着安排）：
{open_close}
- 同一部分里的几个画面要注意场景之间的衔接，前后有一定的叙事性或画面连续性，画面变化的时候要加入衔接画面；
- 换场景时用远景、空镜或一个动作的开始和结束来过渡；同一场景里景别可以由远到近推进，运动方向前后一致；
- 不要用片头片尾字幕、黑场或纯文字画面。

请按用户的要求，从这些卡片里设计一个约 {target} 秒的成片，按顺序写：
1. design：用 2–3 句话写成片的设计：开头、中间、结尾分别用哪些卡片，为什么这样安排；开场怎么交代背景（时间、地点、人物），段落之间怎么过渡；开头、中间、结尾按拍摄时间先后安排；写完对照用户的要求检查内容和先后顺序；
2. opening：写开头第一个画面用哪张卡片、为什么选它；
3. ending：写结尾最后一个画面用哪张卡片、为什么选它、属于哪一种收尾；
4. unmet：要求里提到、但卡片里找不到的内容，都找得到就留空；找不到的内容不要拿别的卡片冒充；
5. parts：照 design 分成 {parts_rule}，按拍摄时间先后排列，每部分里的画面也按拍摄时间先后排列：
   - name 是这一部分的标题，summary 用一句话说明这部分讲什么；
   - link 写这一部分和上一部分怎么衔接：写清上一部分最后一个画面和这一部分第一个画面靠什么接上（同一个人、同一样东西、同一个动作或声音、到达或进入一个地方），不要只写“直接切入”；第一部分写开场怎么进入；
   - scenes 是这一部分要的画面（个数不超过这一段能放的镜头数）：query 用一个短语说明要的画面（如“雨夜车窗上的雨滴”“江边远眺陆家嘴”），写看得见的主体、动作和光线，不写地名（概括里的地名可能认错），所有部分的 query 互不重复；cards 写这个画面取自哪几张卡片（1–3 张），一张卡片只放进一个画面。
只输出 JSON。"""
# B's travel-vlog profile, its 成片结构, offered as a reference (the request still decides).
STRUCTURE = """
这批素材属于「旅行 vlog」，这类成片常用的结构是：开场最抓人的一个画面 → 出发与在路上 → 按地点依次展开，每地一两个最好的镜头 → 最高潮的地点或体验 → 收尾（回望、夜景或离开）。
设计时可以参考，但以用户的要求为准。
"""
# The ending (2026-11-09): what montage and travel editing close on, the user's choice of it, landscapes and
# cities apart; asked for, not ruled out. The Forever cut had ended on a museum's bronze tree.
ENDING = ("结尾的最后一个画面要静下来，节奏放缓，有结束的感觉："
          "素材以自然风景为主时，可以用远去的背影或交通工具、拉远或升高的大景、黄昏落日或星空、几乎不动的风景空镜、人物回望或静坐望远；"
          "素材以城市为主时，可以用夜景和灯火（街道、天际线、江边）、人流或车流里慢慢走远的背影、从高处俯瞰城市、地标建筑的全景；"
          "两种都可以呼应开头")
OPEN_CLOSE = f"- 开场先交代地点、时代和人物，不要跳切；{ENDING}；"

# 第二步。节拍按能取几秒列出：从关键帧起，至少 SHORTEST，至多 CAP。
SHOTS_SYSTEM = """你是剪辑师。大纲定好了段落和每个段落要的画面，程序定好了每个画面约几秒。你给每个画面挑节拍；程序按音乐把每个画面切成几个镜头，镜头卡在小节上；按你排的顺序挑出每个镜头用的节拍，从节拍开头取，一个画面的几个镜头再按拍摄时间先后放；高潮段每个画面尽量一个镜头，超过 6 秒才在小节上切开。成片约 {target:g} 秒，配音乐，不用原声。高潮段的画面只挑全景、远景的节拍，第一个画面的第一个节拍必须是全景或远景，挑最开阔的那个；画面只有一两个镜头时，放不下卡片里的整个过程：不要默认挑第一个节拍，要从这张卡片的全部节拍里挑出最关键、最精彩的那一刻排在第一。高潮段前的最后一个画面，在符合这个画面内容的节拍里，挑最处在“还没”状态的：还没到、还没看见、还没打开、还没落地。
下面每个画面列出它可以取自的卡片（id、时间、场景、概括）和节拍（节拍 id、景别、能取几秒、画面）。
每个画面写：
- beats：从它的卡片里挑节拍，最合适的排前面，多列一两个备用；一个节拍只用一次；
- shows：一句话写这个画面要呈现什么；
- link：它和上一个画面怎么接，第一个写“开场”；写的衔接要能在你挑的节拍里看到。
挑节拍时，按优先级从高到低：
- 画面后面的描述只是大纲给的方向，大纲并没有深入了解画面细节，不用死守；你要自己保证画面内部的叙事性或画面连贯性，这一条优先级最高；
- 这几张卡片里如果有更合适、更精彩的节拍，按需挑选，这一条优先级其次；
- 同一个视频的镜头连着最多 {run} 个，其中中景、近景、特写连着最多 2 个，免得同一件事的小画面（车里、机舱里、滑车上）一连好几个；
- 相邻镜头景别要有变化，同一主体不要用相同景别接着出现；新地方先远景或全景，再到近处和细节；换地点、换时间用动作、方向、相似的形状或空镜过渡，这一条优先级最后。
unmet：大纲要的、节拍里找不到的画面，没有就写空数组。"""
STYLE_RULES = {"旅行": "按时间或路线走；开头用一个能交代地点的全景；中间每处地方挑最有代表性的画面，远景、人和细节穿插；"
                      "结尾落在一个安静、有余味的画面上，如日落、远景、离开的背影。"}

# 卡片描述（本机视觉模型）。
SHOT_SIZES = ["远景", "全景", "中景", "近景", "特写"]
SYSTEM_PROMPT = (
    "你是视频剪辑助理，负责描述素材片段。只根据画面内容回答，"
    "不要猜测画面里看不到的信息。所有文字用简体中文。"
)
# 方案 B's travel-vlog profile: what to look at.
TRAVEL = {
    "key": "travel-vlog", "name": "旅行 vlog",
    "focus": ("- 地点：每个画面在哪里。\n"
           "- 旅程感：出发、在路上、到达、交通工具、行李、地图和路牌。\n"
           "- 人物：出镜的人在做什么，表情和互动。\n"
           "- 亮点：景点的标志性画面、夜景、特别的体验。")}


SUMMARY_CHARS = 80   # a vlog card holds more than a film's: 40 characters left it out

# #416 (2026-11-08 evening): the summary written last. Asked first, it was written before the frames
# were gone through and left out what only some of them show: 01-013's puppet, 01-014's woman in white
# (order_exp.py). process (#416, 2026-11-09): a short ordered list of what happens, between the beats and
# the summary.
# camera (2026-11-16): where the camera is, asked first. Without the old prompt's indoor-or-outdoor question
# ("车厢是室内"), the 4B wrote boats out of the cruise and whale-watch cards and took pj-038's fjord, filmed
# from a boat at the water's level, for a drone; the plan then put it among the flight's shots. Asked from what
# is seen (three wordings tried on 12 cards, three runs each): every boat card 船上, the helicopter's 飞机或直升机里
# (they had been drones), no drone written anywhere; but a lake seen from the shore is still taken for a boat
# (5 of 6), its summary then a boat trip. Data: 剪辑对比/描述随机性（2026-11-16）/. 室内 (2026-11-16, Insta360):
# a breakfast shot through a restaurant's window had been 地面.
CAMERAS = ["地面", "室内", "车里", "船上", "飞机或直升机里", "无人机航拍", "水里", "看不出"]
PROMPT = (
    "下面 {count} 张图按时间先后取自同一段视频（共 {duration:.0f} 秒），每张图前标了编号和它在这段里的秒数。"
    "请输出 JSON：\n"
    "camera：先判断这一段是在哪里拍的：地面（室外）、室内（房间、餐厅、商店里）、车里、船上、飞机或直升机里、无人机航拍、水里，看不出写看不出。"
    "只按画面里看得见的东西判断：看得见车窗、车头、座椅是车里，在房间、餐厅、商店里隔着窗户拍是室内；看得见舷窗、窗框、玻璃反光或桨叶的影子是飞机或直升机里；"
    "看得见船舷、船头、甲板、栏杆或同船的人是船上；看得见岸、路、栈桥或站在地上的脚下是地面；"
    "只看到水面、山和天空，看不到这些的写看不出，不要猜；看不出时 summary 和 beats 也不要写在船上或航拍；\n"
    "beats：见下面的分组说明；\n"
    "{process}"
    "summary：写完 {before} 再写，按 beats 一句话概括这一段，不超过 {chars} 字，beats 里的关键人物和物件要写到；\n"
    # 2026-11-16: with camera asked first, the 4B copied it into scene (地面, 车里) for 51 of 品杰's 116 cards
    # and 25 of Insta360's 33.
    "scene 是这一段所在的地方，写具体的地点或环境（如街道、餐厅里、教堂前、湖边、峡湾），不要写机位（地面、车里、船上）。"
    "片头片尾字幕、制作人员名单、只有文字或黑屏的画面，what 要写明是字幕、文字或黑屏。\n"
    "这类素材是「{name}」。看片时注意：\n{focus}\n"
)
PROCESS = ("process：写完 beats 再写，按 beats 的先后，用短语写这一段里依次发生的事，"
           "短语个数不超过节拍数，每个短语不超过 10 个字，连着几个节拍是同一件事就合成一个短语；\n")

# One beat a key frame. 2026-11-10 (景别调研): the five shot sizes defined, and shot_size and what read from
# that one frame alone, not with a person seen in another frame.
GROUPED = (
    "这些图是程序在画面变化处取的关键帧，共 {groups} 张，每张是一个节拍，按时间先后排列：{layout}。"
    "beats 按图写，每张图正好一项，顺序和图一致。"
    "shot_size 只看这一张图判断，看画面的主角是环境还是人或物：远景是环境是主角，人很小或没有人（大片风景、城市天际线）；"
    "全景是人和环境都能看得到，人在环境里活动，背后的山、湖、街道仍占画面大部分；中景是人或物成了主角，环境退成背景；"
    "近景是人的上半身或一件物品占满大部分画面；特写是脸、手或物件的局部填满画面。"
    "what 只写这一张图里看得见的主体和动作，不超过 30 字。")
