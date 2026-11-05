# 大肥鱼试验素材

**只能用于非商业试验。** 未取得权利人许可前，不要放进商用产品或对外发布的构建。

## 来源

- 帧序列取自 [QCYTSN/dsh-dafeiyu](https://github.com/QCYTSN/dsh-dafeiyu) 的 `assets/pet/`。它们由 [PC2005-cloud/dsh-pet](https://github.com/PC2005-cloud/dsh-pet) 在提交 `549fff2` 的动画转来，是 AI 生成的“鲸尾女仆”。
- `dafeiyu-*.webp` 由 [`scripts/import-companion-art.py`](../../../../scripts/import-companion-art.py) 生成：裁剪、缩放、抽帧，并把抠图在背景里留下的 alpha ≤ 8 的残留清为全透明（否则浅色页面上会显出灰色方框）。角色画面没有修改。
- 上游提交、各段原始视频的 SHA-256、本仓库每张精灵图的 SHA-256，以及下面许可条款所在 README 的 SHA-256，都记在 `dafeiyu.source.json`。

## 许可

dsh-dafeiyu 的 `ASSET_LICENSE.md` 称这组帧使用 MIT，但原作者 dsh-pet 的 README 只把代码定为 MIT。素材另有约定，以原作者为准：

- **素材**（动画、提示词、源视频）：允许开源使用，禁止商用。
- **二次创作**：衍生、改版、换皮作品，在任何介绍、展示、分发该作品的地方，都要附上原作者地址 <https://github.com/PC2005-cloud/dsh-pet>。所以伙伴小窝底部显示了这个地址。
- **形象**：社区二创的 DeepSeek「大肥鱼」（鲸鱼娘），不是 DeepSeek 官方形象。dsh-pet 现行 README 标注为 [CC BY-NC-SA 4.0](https://creativecommons.org/licenses/by-nc-sa/4.0/deed.zh-hans)（署名、非商业、相同方式共享），“不可以以任何形式商业化”。本目录的精灵图属于改编作品，同样按这个协议使用。
- 只读取上游的清单和帧文件，不执行上游仓库中的任何代码，也不安装它的 DSH 插件。
