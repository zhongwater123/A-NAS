# 墨水屏上虚拟伙伴的黑白小图

`sleep.png`、`idle.png`、`buried.png` 是虚拟伙伴（大肥鱼）在墨水屏右栏的三种姿势：没事睡觉、放得下时等着、放不下时被落叶埋住（[墨水屏规格](../../../docs/specs/eink-screens.md#看板待办)）。

- 形象与授权和桌面上的虚拟伙伴相同，署名见 `web/src/companion/credit.ts`（设置 → 概览的最后一行）。
- 来源：水墨屏原型的 `proto/screen/pet/{sleep,idle,buried}.png`（由原型的 `pet_assets.py` 从 dsh-pet 提交 10f6c86 的 `assets/webm/` 各取一帧生成，黑白红三色），再按原型黑白屏的画法转成 1 位图：落叶描黑边、里面画斜线。
- 原型图的 SHA-256 前 16 位：`sleep` 4d0e43b258e3adaf、`idle` 46748cd5d9181087、`buried` 371ba1c9554f56bc。
- 本目录图的 SHA-256：
  - `sleep.png` ebfdb2e80d3fa724144311265698509cd0c1c239172033d12022fa64f9d628ac
  - `idle.png` 9fa2e84f57c85c6309514d1db64b2aacb9d979fc0012960a3bf6362b51db6cd3
  - `buried.png` 13e58739fc1c5865282b470eab66d4b2c48f8e510b53c48384a8c3a1cc674483
- 换形象时只替换这三张图（最高 180、最宽 176 像素，黑为 0、白为 255），姿势规则不变。
