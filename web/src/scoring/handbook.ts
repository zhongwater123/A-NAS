// The landscape scoring handbook as the scoring guide shows it
// (Lora-SFT docs/scoring-guideline.html, v1.3, final), with the 打分项 the
// user chose on 2026-11-26: 构图, 光影, 叙事性 and 主体, then 总体 last, in
// place of 画质 and 情绪 (decision 30 of
// docs/decisions/2026-11-12-photo-scoring.md). Only what raters act
// on is here: the project's own checks (the expected spread of scores,
// calibration and spot checks, the version history) and what this module
// does not ask for (keep, problem tags, comments, burst groups) are left
// out. The example images come from the public EVA dataset and only
// illustrate levels 1, 3 and 5 until calibration replaces them with the
// project's own. EVA's repository is CC0-1.0, but its photos are AVA's
// resized DPChallenge photos, whose rights stay with their photographers;
// the user accepted shipping them (decision 28); those of 画质 and 情绪
// illustrate 叙事性 and 主体 now.

import compositionLow from "./tutorial/composition-1.jpg";
import compositionMid from "./tutorial/composition-3.jpg";
import compositionHigh from "./tutorial/composition-5.jpg";
import lightLow from "./tutorial/light_color-1.jpg";
import lightMid from "./tutorial/light_color-3.jpg";
import lightHigh from "./tutorial/light_color-5.jpg";
import overallLow from "./tutorial/overall-1.jpg";
import overallMid from "./tutorial/overall-3.jpg";
import narrativeLow from "./tutorial/narrative-1.jpg";
import narrativeMid from "./tutorial/narrative-3.jpg";
import narrativeHigh from "./tutorial/narrative-5.jpg";
import overallHigh from "./tutorial/overall-5.jpg";
import subjectLow from "./tutorial/subject-1.jpg";
import subjectMid from "./tutorial/subject-3.jpg";
import subjectHigh from "./tutorial/subject-5.jpg";

// An example photo: the handbook's level for it and why.
export interface Example {
  level: 1 | 3 | 5;
  image: string;
  // The image's own size, which the stage fits the photo by.
  width: number;
  height: number;
  alt: string;
  why: string;
}

// What the handbook says about one 打分项, matched to a batch's by id.
export interface DimensionGuide {
  id: string;
  name: string;
  // A word beside the name, such as 最后打.
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
  "浏览器缩放保持 100%，按正常大小看整张照片，不点 100% 查看、不放大找细节。",
  "每打 1 小时休息 10 分钟，每天建议不超过 3 小时。人累了打分会越来越松或越来越严。",
  "打分是一个人独立完成的，打完之前不要和别人讨论具体照片。",
];

// steps is what to do with each photo of a batch that scores these 打分项 in
// this order. The handbook scores 总体 last, after the 打分项 it is more than
// the sum of; a batch made before, which scores 总体 first on the first
// impression, and any other order are scored as the stage asks for them.
export function steps(dimensions: readonly string[]): string[] {
  const several = dimensions.length > 1;
  const first =
    dimensions[0] === "overall"
      ? [
          "看整张 3–5 秒，凭第一感觉打“总体”：拍得好不好、有没有让人想多看的感觉。",
          ...(several ? ["再按顺序打其余各项：每项只看它自己的那一方面，下面写了每项看什么、不看什么。"] : []),
        ]
      : [
          several
            ? "先看整张，再按顺序逐项打：每项只看它自己的那一方面，下面写了每项看什么、不看什么。"
            : "只看这一项自己的那一方面，下面写了它看什么、不看什么。",
          ...(several && dimensions.at(-1) === "overall"
            ? ["最后打“总体”：不是前面几项的平均。看完各项，你有多想多看一会儿这张照片。"]
            : []),
        ];
  return [...first, "一张照片大约 1 分钟。不要过度纠结，在两个分之间犹豫时，选更接近第一感觉的那个。"];
}

// A rule of the handbook. fiveOnly ones are shown only on five levels; three
// replaces the text on three levels, which have no 2 or 4.
export interface Rule {
  title: string;
  text: string;
  three?: string;
  fiveOnly?: boolean;
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
  { title: "只看画面", text: "不考虑拍摄难度、地点有没有名、用的什么手机、是不是你自己拍的。" },
  { title: "按大多数人的眼光", text: "个人口味（比如不喜欢雪景、偏爱黑白）不加分也不扣分。" },
];

export const dimensionGuides: DimensionGuide[] = [
  {
    id: "composition",
    name: "构图",
    question: "画面安排得好不好？",
    about: "视线有没有地方落、有没有路可走，画面整不整齐。",
    ignore: "主体突不突出（那是主体），光线和颜色，清不清楚。",
    cues: [
      "地平线水平",
      "前景、中景、远景",
      "引导线：路、河、岸线",
      "裁切：山顶、楼顶被切",
      "边缘杂物",
      "天空和地面的比例",
    ],
    levels: [
      "层次丰富，视线有引导，边缘干净",
      "构图用心，有一两处小问题",
      "端正，平铺直叙",
      "地平线略歪，或边缘有杂物，或比例别扭",
      "明显歪斜，该有的被切掉，画面杂乱",
    ],
    examples: [
      {
        level: 5,
        image: compositionHigh,
        width: 560,
        height: 372,
        alt: "雪山和前景水洼",
        why: "前景水洼把视线引向远山",
      },
      {
        level: 1,
        image: compositionLow,
        width: 560,
        height: 323,
        alt: "倾斜的桥梁阴影",
        why: "画面倾斜，不知道看哪里",
      },
      {
        level: 3,
        image: compositionMid,
        width: 560,
        height: 348,
        alt: "水边的房子",
        why: "端正，但平铺直叙，没有层次",
      },
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
    name: "光影",
    question: "光线和颜色好不好看？",
    about: "明暗和颜色是否合适。",
    ignore: "构图和主体。雾、霾的朦胧属于这一项，不算毛病。",
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
      { level: 1, image: lightLow, width: 560, height: 386, alt: "发灰的山和水", why: "发灰发白，没有明暗" },
      { level: 5, image: lightHigh, width: 560, height: 481, alt: "海上的日落光芒", why: "云隙光和暖色，明暗出彩" },
      { level: 3, image: lightMid, width: 560, height: 425, alt: "正常日光下的农场", why: "曝光正常，光线平平" },
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
    id: "narrative",
    name: "叙事性",
    question: "这张照片有没有在讲一件事、一种处境？",
    about: "看完说不说得出它在讲什么：一个时刻、一种天气里的处境、人和自然的关系、季节的变化。",
    ignore: "拍得好不好看、构图和光线；地点有没有名。",
    cues: [
      "人迹与生命：人、船、动物、炊烟",
      "正在发生的事：暴风雨要来、日出那一刻",
      "时刻与季节：初雪、秋叶、新绿",
      "对比：渺小与辽阔、新与旧",
      "看完记得住什么",
    ],
    levels: [
      "一眼看出在讲什么，看完还会想：人和风景之间有处境、有故事",
      "有明确想说的事，比如一个时刻、一种天气里的感受",
      "能看出在哪、什么天气，但说不出它想讲什么",
      "内容平淡，只是一片景",
      "看不出想拍什么",
    ],
    examples: [
      {
        level: 3,
        image: narrativeMid,
        width: 560,
        height: 522,
        alt: "草坡上吃草的马",
        why: "有马、有滑翔伞，但说不出想讲什么",
      },
      { level: 1, image: narrativeLow, width: 560, height: 374, alt: "模糊的山坡和鸟群", why: "看不出想拍什么" },
      {
        level: 5,
        image: narrativeHigh,
        width: 560,
        height: 336,
        alt: "崖边的人看着暴风雨下的海",
        why: "一个人站在崖边面对风暴，看完记得住",
      },
    ],
    pitfallsTitle: "容易打错",
    pitfalls: [
      "叙事性不等于有人：空无一人的雪原也可以讲“冬天来了”。",
      "不是景点越有名分越高，看照片本身讲了什么。",
      "说不出它在讲什么时，一般不超过 3 分。",
    ],
  },
  {
    id: "subject",
    name: "主体",
    question: "有没有一眼就看到的主体，它突不突出？",
    about: "第一眼落在哪，那个东西和背景分不分得开。",
    ignore: "主体放在哪个位置、画面怎样安排（那是构图）；主体本身好不好看。",
    cues: [
      "一眼先看到什么",
      "主体和背景：明暗、颜色、虚实分没分开",
      "抢眼的杂物",
      "主体太小、被挡住",
      "几样东西在抢注意",
    ],
    levels: [
      "主体一眼就看到，和背景分得很开，没有东西抢",
      "主体清楚，有一点干扰",
      "有主体，但不够突出，或有别的东西分走注意",
      "要找一下才知道拍的是什么",
      "找不到主体，画面散",
    ],
    examples: [
      {
        level: 1,
        image: subjectLow,
        width: 560,
        height: 292,
        alt: "湖、山和小镇的全景",
        why: "湖、山、小镇平铺，找不到一眼看到的",
      },
      { level: 5, image: subjectHigh, width: 470, height: 720, alt: "雾中的树干", why: "雾隐去了背景，树干一眼就看到" },
      {
        level: 3,
        image: subjectMid,
        width: 560,
        height: 379,
        alt: "岩壁和溪流",
        why: "岩壁是主体，但右边的山和前景的石头分走注意",
      },
    ],
    pitfallsTitle: "容易打错",
    pitfalls: [
      "大场景也可以有主体：一座山、一棵树、一道光。",
      "主体不一定在中间，放在哪里是构图的事。",
      "整片花海、整片云这样以“一片”为主体的，看它是否撑满画面、有没有东西抢。",
    ],
  },
  {
    id: "overall",
    name: "总体",
    note: "最后打",
    question: "看完各项，这张照片整体拍得好不好，有多想多看一会儿？",
    about: "前面几项之外的东西：第一眼的吸引力、氛围、能不能打动人，以及一处毛病对整张的伤害。",
    ignore: "拍摄难度、地点名气、器材；放大到原尺寸才看得出的细节。",
    cues: ["第一眼的吸引力", "氛围能不能打动人", "想不想发给别人看", "一处毛病有多伤整张：糊了、歪得厉害"],
    levels: [
      "想打印出来挂墙、想发给别人看；氛围或故事强烈，让人有感触",
      "拍得不错，有明显的亮点或明确的氛围",
      "普通记录照，能看出在哪、什么天气，但没有特别的感受",
      "有明显问题，或内容平淡、看不出想拍什么",
      "废片：没有内容、像误拍，或整张糊得看不清",
    ],
    examples: [
      { level: 3, image: overallMid, width: 560, height: 420, alt: "树下的旧房子", why: "普通，没有特别的亮点" },
      {
        level: 5,
        image: overallHigh,
        width: 560,
        height: 394,
        alt: "雪山和湖面倒影",
        why: "山、湖、云层次分明，想多看",
      },
      { level: 1, image: overallLow, width: 560, height: 315, alt: "灰蒙蒙的城市", why: "灰蒙蒙，看不出想拍什么" },
    ],
    pitfallsTitle: "容易打错",
    pitfalls: [
      "总体不是前面几项的平均，可以比哪一项都高或都低。不要算平均分，问自己有多想多看一会儿。",
      "清不清楚没有单独的项：糊、抖、噪点影响观看时，在总体里扣。一处毛病很重可以把总体拉得很低。",
      "氛围不需要有人，空无一人的雾中树林也可以很有意境。",
      "说不出这张照片给你什么感觉时，一般不超过 3 分。",
    ],
  },
];

// What to do in cases the scale does not cover, with this module's keys.
export const specialCases: [string, string][] = [
  ["画面里有人", "人很小、只是风景的一部分：正常打，人迹可以算叙事性的看点。人占画面主体：按 S“无法判断”跳过。"],
  ["黑白照片", "正常打。光影只看明暗层次。"],
  ["长曝光", "流水丝滑、车流光轨是有意的，不算毛病。"],
  ["夜景", "按夜景的标准看：灯光和明暗层次。暗本身不扣分。"],
  ["后期很重", "HDR 感、颜色过艳，在光影里扣分。"],
  ["截图、水印、拼图、全景", "不应该出现。按 X“图片有问题”。"],
  ["认识这个地方或拍摄人", "照常按画面打，不加分不减分。"],
  ["两个分之间犹豫", "选更接近第一感觉的那个。不要每次都退回 3 分。"],
];

export const skipping =
  "只有照片根本没法按风景来打时才跳过：不是风景、图片损坏打不开、截图或拼图。拿不准分数不算跳过的理由。";
