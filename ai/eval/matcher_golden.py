"""Writes the expected matches of the object class matcher for the Go tests.

The photo service matches queries to detector classes in Go
(internal/photos/objects); the evaluation measured the Python matcher in
eval.hybrid_search. This writes what the Python matcher finds for every
vocabulary label and a set of family-photo queries, so that the Go test can
check that both agree.

    python -m eval.matcher_golden
"""

import json
import os

from eval import datasets
from eval.hybrid_search import Matcher, coverage

GOLDEN = os.path.join(datasets.REPO, "internal", "photos", "objects", "testdata", "matcher_golden.json")

QUERIES = [
    "手机", "桌上的手机", "海边的狗", "生日蛋糕和蜡烛", "一碗面条", "派对上的气球", "马路上的车", "热狗和薯条",
    "猫头鹰", "两只猫在沙发上", "孩子在滑梯上玩", "厨房里的电饭煲", "包子和饺子", "阳台上的盆栽", "穿运动鞋的人",
    "红色的杯子", "笔记本电脑和鼠标", "小狗在草地上跑", "爸爸抱着宝宝", "去年夏天在海边", "火车站", "停车场里的汽车",
    "鸟在树上", "一群鸭子在水里", "冰箱里的水果", "放在床头柜上的台灯", "地上的鞋子", "拿着雨伞的女孩", "公交车和自行车",
    "吃火锅", "截图", "身份证", "猫咪", "苹果手机", "乒乓球拍", "网球比赛", "滑雪的人", "下面的盘子", "上面有一本书",
    "Model 3D 1024", "iPad 和键盘", "SUV", "F1赛车", "宠物", "海鲜大餐", "水果拼盘", "家电", "乐器", "球场上的足球",
    "地球仪", "一个男人站在厨房里切菜，旁边放着一个锅", "两个孩子坐在餐桌前吃蛋糕，桌上有蜡烛和气球",
    "一只猫躺在键盘上，旁边是一杯咖啡", "“海边”的日落！", "  手机  ", "",
]


def main():
    matcher = Matcher()
    queries = [label["name"] for label in datasets.load_vocabulary()] + QUERIES
    cases = []
    for query in dict.fromkeys(queries):
        mentions = matcher.mentions(query)
        cases.append({"query": query,
                      "mentions": [{"word": word, "classes": sorted(ids)} for word, ids in mentions],
                      "coverage": round(coverage(query, mentions), 6)})
    os.makedirs(os.path.dirname(GOLDEN), exist_ok=True)
    with open(GOLDEN, "w", encoding="utf-8", newline="\n") as file:
        json.dump(cases, file, ensure_ascii=False, indent=1)
        file.write("\n")
    print(f"{len(cases)} queries written to {GOLDEN}")


if __name__ == "__main__":
    main()
