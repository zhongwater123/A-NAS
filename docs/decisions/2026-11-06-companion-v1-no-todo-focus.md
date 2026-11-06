# 虚拟伙伴 V1 是否不含待办与专注计时

状态：pending
更新时间：2026-11-06

## 问题

[#404](https://github.com/zhongwater123/A-NAS/issues/404) 之前的电脑桌宠原型带有本机待办与专注计时：待办按成员存在本机，靠固定说法识别“记一下”“提醒我”之类的话；专注计时在伙伴上倒数。#404 的审查意见指出，待办应存在 NAS 上，不应按成员存在本机，固定说法匹配等于手写意图解析。[PR #412](https://github.com/zhongwater123/A-NAS/pull/412) 按 V1 范围没有做这两项，[规格](../specs/virtual-companion.md)的非目标中也写明不做；这一范围需要确认。不定的话，V1 的验收范围与后续由谁来做待办都不明确。

## 选项

- **V1 不含待办与专注计时**：伙伴只表现 VNAS 的状态、提醒与 VNAS 下达的动作。待办作为 VNAS 的能力另行规划，存在 NAS 上，由模型理解说法；专注计时以后再议。代价：旧原型中的这两项功能暂时没有。
- **V1 含待办，存在 NAS 上**：需要新的数据与接口，与 [#414](https://github.com/zhongwater123/A-NAS/pull/414) 记录的 2026-11-06 决定中“伙伴不另设数据与接口、命令经 VNAS 下达”不一致，并扩大 V1 的验收范围。
- **V1 含专注计时**：只在页面内计时，不新增数据；需要补规格与测试，扩大 V1 的验收范围。

## 决定

定下后填写。

## 关联

- 规格：[虚拟伙伴（大肥鱼）](../specs/virtual-companion.md)的非目标
- 实现：[PR #412](https://github.com/zhongwater123/A-NAS/pull/412)
- 提议与审查：[#404](https://github.com/zhongwater123/A-NAS/issues/404)
- 虚拟伙伴（大肥鱼）V1 的形象、实现方式与 VNAS 的控制：[#414](https://github.com/zhongwater123/A-NAS/pull/414)
