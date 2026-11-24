// The landscape scoring handbook as the tutorial shows it before a batch
// (Lora-SFT docs/scoring-guideline.html, v1.3, final). Only what raters act
// on is here: the project's own checks (the expected spread of scores,
// calibration and spot checks, the version history) and what this module
// does not ask for (keep, problem tags, comments, burst groups) are left
// out. The example images come from the public EVA dataset and only
// illustrate levels 1, 3 and 5 until calibration replaces them with the
// project's own. EVA's repository is CC0-1.0, but its photos are AVA's
// resized DPChallenge photos, whose rights stay with their photographers;
// the user accepted shipping them (decision 28 of
// docs/decisions/2026-11-12-photo-scoring.md).

import compositionLow from "./tutorial/composition-1.jpg";
import compositionMid from "./tutorial/composition-3.jpg";
import compositionHigh from "./tutorial/composition-5.jpg";
import emotionLow from "./tutorial/emotion-1.jpg";
import emotionMid from "./tutorial/emotion-3.jpg";
import emotionHigh from "./tutorial/emotion-5.jpg";
import lightLow from "./tutorial/light_color-1.jpg";
import lightMid from "./tutorial/light_color-3.jpg";
import lightHigh from "./tutorial/light_color-5.jpg";
import overallLow from "./tutorial/overall-1.jpg";
import overallMid from "./tutorial/overall-3.jpg";
import overallHigh from "./tutorial/overall-5.jpg";
import qualityLow from "./tutorial/quality-1.jpg";
import qualityMid from "./tutorial/quality-3.jpg";
import qualityHigh from "./tutorial/quality-5.jpg";

// An example photo: the handbook's level for it and why.
export interface Example {
  level: 1 | 3 | 5;
  image: string;
  alt: string;
  why: string;
}

// What the handbook says about one 打分项, matched to a batch's by id.
export interface DimensionGuide {
  id: string;
  name: string;
  // A word beside the name, such as 描述，不是好坏.
  note?: string;
  question: string;
  about: string;
  ignore: string;
  cues: string[];
  // What each level looks like, from 5 down to 1.
  levels: [string, string, string, string, string];
  // In the order the tutorial asks for them, so that it does not give the
  // answers away.
  examples: Example[];
  pitfallsTitle: string;
  pitfalls: string[];
}

export const preparation = [
  "用电脑屏幕打分，不要用手机。屏幕亮度调到看网页舒服的程度，避免阳光直射屏幕。",
  "浏览器缩放保持 100%，按正常大小看整张照片。画质也按正常大小判断，不点 100% 查看。",
  "每打 1 小时休息 10 分钟，每天建议不超过 3 小时。人累了打分会越来越松或越来越严。",
  "打分是一个人独立完成的，打完之前不要和别人讨论具体照片。",
];

// steps is what to do with each photo of a batch that scores these 打分项 in
// this order. The handbook scores 总体 first, on the first impression; a
// batch that asks for another 打分项 first is scored in its own order, as
// the stage asks for them.
export function steps(dimensions: readonly string[]): string[] {
  const several = dimensions.length > 1;
  const first =
    dimensions[0] === "overall"
      ? [
          "看整张 3–5 秒，凭第一感觉打“总体”：拍得好不好、有没有让人想多看的感觉。先打总体，是为了不被后面的细节分析带偏。",
          ...(several ? ["再按顺序打其余各项：每项只看它自己的那一方面，下面写了每项看什么、不看什么。"] : []),
        ]
      : [
          several
            ? "按顺序逐项打：每项只看它自己的那一方面，下面写了每项看什么、不看什么。"
            : "只看这一项自己的那一方面，下面写了它看什么、不看什么。",
        ];
  return [...first, "一张照片大约 1 分钟。不要过度纠结，在两个分之间犹豫时，选更接近第一感觉的那个。"];
}

// A rule of the handbook. fiveOnly ones are shown only on five levels,
// dimension ones only to a batch that scores it; three replaces the text on
// three levels, which have no 2 or 4.
export interface Rule {
  title: string;
  text: string;
  three?: string;
  fiveOnly?: boolean;
  dimension?: string;
}

export const rules: Rule[] = [
  {
    title: "只打整数",
    text: "每项只打 1、2、3、4、5 五档，没有小数。拿不准时选更接近第一感觉的那一档；更细的差别由几个人的分数合起来得出，不用自己细分。",
    fiveOnly: true,
  },
  {
    title: "3 分是普通手机照",
    text: "没有大毛病、也没有亮点的随手记录照打 3 分。所有项都以这个为中点往上往下打。",
  },
  {
    title: "加分扣分要有依据",
    text: "打 4、5 分，要说得出好在哪；打 1、2 分，要有看得见的毛病。说不出依据的，就打 3 分。",
    three: "打 5 分，要说得出好在哪；打 1 分，要有看得见的毛病。说不出依据的，就打 3 分。",
  },
  {
    title: "用满 1–5 分",
    text: "好照片就给 5，废片就给 1，不要只在 3、4 之间打。",
    three: "好照片就给 5，废片就给 1，不要总打 3。",
  },
  {
    title: "每项单独看",
    text: "喜欢一张照片，不代表它每项都高；一项很差，也不代表其他项都差。同一个毛病只在它所属的那一项扣分。",
  },
  {
    title: "情绪是描述",
    text: "其他各项是好坏，情绪只说平静还是激烈，3 分是中间，不是一般。“用满 1–5 分”不适用于情绪。",
    dimension: "emotion",
  },
  { title: "只看画面", text: "不考虑拍摄难度、地点有没有名、用的什么手机、是不是你自己拍的。" },
  { title: "按大多数人的眼光", text: "个人口味（比如不喜欢雪景、偏爱黑白）不加分也不扣分。" },
];

export const dimensionGuides: DimensionGuide[] = [
  {
    id: "overall",
    name: "总体",
    note: "主分",
    question: "这张照片整体拍得好不好，有没有让人想多看一会儿的感觉？",
    about: "第一眼的吸引力，加上它在讲什么、给人什么感觉。",
    ignore: "拍摄难度、地点名气、器材；放大到原尺寸才看得出的细节。",
    cues: [
      "第一眼的吸引力",
      "天气：暴风雨、雾、雪",
      "时刻：日出、彩虹、闪电",
      "季节感：秋叶、新绿",
      "人迹与生命：远处行人、船、鸟、炊烟",
      "氛围能不能打动人",
    ],
    levels: [
      "想打印出来挂墙、想发给别人看；氛围或故事强烈，让人有感触",
      "拍得不错，有明显的亮点或明确的氛围",
      "普通记录照，能看出在哪、什么天气，但没有特别的感受",
      "有明显问题，或内容平淡、看不出想拍什么",
      "废片：没有内容、像误拍，或整张糊得看不清",
    ],
    examples: [
      { level: 3, image: overallMid, alt: "树下的旧房子", why: "普通，没有特别的亮点" },
      { level: 5, image: overallHigh, alt: "雪山和湖面倒影", why: "山、湖、云层次分明，想多看" },
      { level: 1, image: overallLow, alt: "灰蒙蒙的城市", why: "灰蒙蒙，看不出想拍什么" },
    ],
    pitfallsTitle: "容易打错",
    pitfalls: [
      "总体不是其他几项的平均。一项特别差（比如整张糊了）可以把总体拉得很低。",
      "不要先打好其他几项再“推”总体，总体在第一步凭感觉打。",
      "情绪激烈不等于总体高，平静的照片也可以打 5 分。",
      "氛围不需要有人，空无一人的雾中树林也可以很有意境。",
      "不是景点越有名分越高，看照片本身传达了什么。",
      "说不出这张照片给你什么感觉时，一般不超过 3 分。",
    ],
  },
  {
    id: "composition",
    name: "构图",
    question: "画面安排得好不好，主体清不清楚？",
    about: "视线有没有地方落、有没有路可走。",
    ignore: "清不清楚、光线和颜色。照片糊了扣画质，不扣构图。",
    cues: [
      "地平线水平",
      "主体明确",
      "前景、中景、远景",
      "引导线：路、河、岸线",
      "裁切：山顶、楼顶被切",
      "边缘杂物",
      "天空和地面的比例",
    ],
    levels: [
      "主体明确，层次丰富，视线有引导，边缘干净",
      "构图用心，有一两处小问题",
      "端正，主体居中，平铺直叙",
      "地平线略歪，或主体不明确，或边缘有杂物",
      "明显歪斜，主体被切掉，画面杂乱",
    ],
    examples: [
      { level: 5, image: compositionHigh, alt: "雪山和前景水洼", why: "前景水洼把视线引向远山" },
      { level: 1, image: compositionLow, alt: "倾斜的桥梁阴影", why: "画面倾斜，不知道看哪里" },
      { level: 3, image: compositionMid, alt: "水边的房子", why: "端正，但主体和背景混在一起" },
    ],
    pitfallsTitle: "容易打错",
    pitfalls: [
      "居中构图不一定差，对称的倒影照片居中反而最好。",
      "三分法不是硬规定，不按三分法不扣分。",
      "海平面、湖面歪 2° 以上就看得出来，这种要扣分；山坡本身是斜的不算歪。",
    ],
  },
  {
    id: "light_color",
    name: "光影色彩",
    question: "光线和颜色好不好看？",
    about: "明暗和颜色是否合适。",
    ignore: "构图和画质。雾、霾的朦胧属于这一项，不算画质问题。",
    cues: [
      "光线方向：侧光、顺光、逆光",
      "光的软硬",
      "天空是否发白",
      "暗部是否死黑",
      "整体偏亮偏暗",
      "偏蓝偏黄",
      "颜色过艳、HDR 感过重",
    ],
    levels: [
      "光线出彩（日出日落、云隙光、雾中光），明暗和色彩都好",
      "光线好，有小瑕疵，比如天空局部发白",
      "正常日光，曝光正确但平淡",
      "天空大面积发白或暗部太黑，或者偏色明显",
      "严重过曝或欠曝，颜色怪异",
    ],
    examples: [
      { level: 1, image: lightLow, alt: "发灰的山和水", why: "发灰发白，没有明暗" },
      { level: 5, image: lightHigh, alt: "海上的日落光芒", why: "云隙光和暖色，明暗出彩" },
      { level: 3, image: lightMid, alt: "正常日光下的农场", why: "曝光正常，光线平平" },
    ],
    pitfallsTitle: "容易打错",
    pitfalls: [
      "阴天不一定低分，柔和的光和氛围也可以好看。",
      "黑白照片只按明暗层次打，不因为没有颜色扣分。",
      "夜景暗是正常的，看灯光和明暗层次，不按白天的亮度要求。",
      "颜色越艳不等于越好，假得像画的要扣分。",
    ],
  },
  {
    id: "quality",
    name: "画质",
    question: "按正常大小看，有没有看得出的画质毛病？",
    about: "照片在窗口里完整显示时就看得出的抖动、虚焦、噪点。不点 100% 查看，不放大找细节。",
    ignore: "构图、光线和颜色；只有放大才看得出的细节。有意的长曝光和背景虚化不扣分。",
    cues: ["对焦：主体实不实", "手抖：双重边缘、拖影", "噪点：暗部彩色颗粒", "涂抹感：树叶草地像油画", "压缩块、色带"],
    levels: [
      "干净利落，看不出任何毛病",
      "仔细看能发现轻微发软或少量噪点",
      "有些发软或噪点，但不影响看清内容",
      "一眼就看出发软、手抖或噪点",
      "整张糊，看不清",
    ],
    examples: [
      { level: 3, image: qualityMid, alt: "湖和远山", why: "远处偏软，但内容清楚" },
      { level: 1, image: qualityLow, alt: "模糊的山坡", why: "整张糊，前景拖影" },
      { level: 5, image: qualityHigh, alt: "岩壁和溪流", why: "岩壁、树木的细节都清楚" },
    ],
    pitfallsTitle: "这些不扣画质",
    pitfalls: [
      "有意的长曝光：瀑布、流水拍成丝绸状。",
      "雾、霾、水汽造成的朦胧，这算光影色彩，不算画质问题。",
      "有意的背景虚化，只要主体清楚。",
      "屏幕小或分辨率低时看不清细节，不算照片的毛病：看不出毛病就按没有毛病打。",
    ],
  },
  {
    id: "emotion",
    name: "情绪",
    note: "描述，不是好坏",
    question: "画面给人的感觉是平静还是激烈？",
    about: "只看情绪有多强、多紧张，不看拍得好不好。3 分是不平静也不激烈，不代表一般。",
    ignore: "照片好不好看、技术好坏。平静的照片可以总体 5 分、情绪 1 分。",
    cues: [
      "天气：暴风雨、雷电、狂风暴雪",
      "水：惊涛、急流、飞溅",
      "天空：翻涌的云、压城的乌云",
      "光：强烈的明暗对比",
      "平静：薄雾、静水、空旷的雪原",
    ],
    levels: [
      "激烈紧张：暴风雨、惊涛拍岸、雷电、狂风暴雪",
      "有明显的动感或压迫感：翻涌的云、湍急的河、阴云压顶",
      "不平静也不激烈：普通天气下的山水",
      "安静：微风、缓流、柔和的光",
      "非常平静，几乎凝固：静水倒影、雾中空林、雪后旷野",
    ],
    examples: [
      { level: 5, image: emotionHigh, alt: "乌云下浪打礁石", why: "乌云压顶，浪打礁石，让人紧张" },
      { level: 3, image: emotionMid, alt: "晴天的绿色草坡", why: "晴天的草坡，平常的感觉" },
      { level: 1, image: emotionLow, alt: "雾中树林倒映在静水里", why: "雾中树林倒映在静水里，几乎凝固" },
    ],
    pitfallsTitle: "容易打错",
    pitfalls: [
      "情绪不是好坏，不要把喜欢的照片往高处打。",
      "肃杀的画面看有没有张力：寒风、乌云、暴雪让人紧张的打 4–5；静止的枯林、荒原打 1–2。",
      "拿不准时问自己：看这张照片，心里是静下来还是绷起来。",
    ],
  },
];

// What to do in cases the scale does not cover, with this module's keys.
export const specialCases: [string, string][] = [
  ["画面里有人", "人很小、只是风景的一部分：正常打，人迹可以算总体的看点。人占画面主体：按 S“无法判断”跳过。"],
  ["黑白照片", "正常打。光影色彩只看明暗层次。"],
  ["长曝光", "流水丝滑、车流光轨是有意的，不扣画质。"],
  ["夜景", "按夜景的标准看：灯光、层次、噪点。暗本身不扣分。"],
  ["后期很重", "HDR 感、颜色过艳，在光影色彩里扣分。"],
  ["截图、水印、拼图、全景", "不应该出现。按 X“图片有问题”。"],
  ["认识这个地方或拍摄人", "照常按画面打，不加分不减分。"],
  ["两个分之间犹豫", "选更接近第一感觉的那个。不要每次都退回 3 分。"],
];

export const skipping =
  "只有照片根本没法按风景来打时才跳过：不是风景、图片损坏打不开、截图或拼图。拿不准分数不算跳过的理由。";
