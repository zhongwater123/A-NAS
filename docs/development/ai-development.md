# AI 开发与评估

更新时间：2026-10-27

本文记录相册 AI 的模型测试、运行环境打包、数据集与评估命令。以下路径示例以开发机 B 为准；macOS 的模型位置和导出限制见[开发机 C](environments/macos.md)。普通门禁与服务隔离检查见[测试与验证](testing.md)，实验 NAS 上启用 AI 使用[运行手册](../runbooks/enable-photo-ai.md)。

## 模型测试与运行环境

相册 AI Worker（`ai/`）的协议测试只需 Python 标准库：`make ai-test`。使用真实 EmbeddingGemma 2 与 D-FINE 检测器的测试需要 MediaPipe 与 ONNX Runtime，在 Debian 13 开发镜像中运行（镜像已按锁文件的版本装好 ONNX Runtime），模型从开发机预留位置只读挂载（见 [M2 实施方案](../architecture/photo-ai.md#模型与运行环境的交付)）；检测器是用官方权重导出的 `D:\A-NAS-models\dfine-obj365\official\dfine_m_obj365.onnx`（[研究](../research/photo-ai-object-detection.md)）：

```bash
docker build -t anas-ai-dev:trixie -f ai/dev.Dockerfile ai      # 基于 anas-systemd:trixie
docker run --rm --cpuset-cpus=0-3 -v "$PWD:/src:ro" -w /src/ai \
  -v /mnt/d/A-NAS-models/embeddinggemma-2-740m:/models:ro -v /mnt/d/A-NAS-models/dfine-obj365/official:/detector:ro \
  -e ANAS_AI_MODEL=/models/embeddinggemma-2-740m.litertlm -e ANAS_AI_DETECTOR=/detector/dfine_m_obj365.onnx \
  -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev:trixie /opt/anas-ai/bin/python -m unittest discover -s tests -t .
```

发行用的 Worker 运行环境（[ADR 0016](../adr/0016-ship-photo-ai-as-a-built-in-offline-capability.md)）也在开发机上构建：`scripts/build-ai-runtime.sh` 在同一开发镜像中用 uv 按 [`ai/requirements.txt`](../../ai/requirements.txt) 的哈希安装 wheel，再运行 `pip check`，打包为 `build/ai-runtime/<ID>.tar.gz`，并写出 `deploy-dev.ps1` 读取的 `build/ai-runtime.json`。同一 ID 的包已存在时直接复用，因此 NAS 上看到的哈希不变。锁文件由 `ai/requirements.in` 生成：改了它之后，或要升级某个包时，运行 `scripts/build-ai-runtime.sh --lock [--upgrade-package 包名]`，它用 `uv pip compile` 重新解析运行环境与开发工具的两个锁文件。两者都需要 `ai/requirements-dev.txt` 固定版本的 uv 与 jq，都从 PyPI 下载，NAS 不需要。2026-10-10 的包 `ccd886271b067afa` 压缩后 163 MB，解开约 460 MB；2026-10-13 加入 ONNX Runtime 后为 `3eca142df9a765c2`，压缩后 189 MB，解开约 542 MB。

## 数据集与评估

AI 的评估（`ai/eval/`：标签阈值校准、搜索取舍与分块向量）使用放在 `D:\A-NAS-datasets`（WSL 中为 `/mnt/d/A-NAS-datasets`）的公开数据集，只在开发机使用，不进入仓库、NAS 或发行包；数据集来源、许可与结论见[标签校准报告](../research/photo-ai-label-calibration.md)。COCO 与 Open Images 元数据按报告中的地址下载并解压后，依次逐张下载其余图片、计算向量、校准阈值：

```bash
AI="docker run --rm -v $PWD:/src -w /src/ai -v /mnt/d/A-NAS-datasets:/datasets \
  -v /mnt/d/A-NAS-models/embeddinggemma-2-740m:/models:ro -v $HOME/.cache/anas-ai-xnnpack:/xnnpack \
  -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev:trixie /opt/anas-ai/bin/python"
$AI -m eval.fetch --budget-gb 4                       # 先用 HEAD 计算体积，超出预算不下载
$AI -m eval.embed --model /models/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --tokens 70
$AI -m eval.calibrate --cache "/datasets/cache/<模型 ID>-e512" --report /datasets/report.json --write
```

`eval.embed` 可中断，重跑时只补算缺少的向量；权重缓存放在 WSL 自己的磁盘上，加载比放在 `/mnt/d` 快。`--write` 不带路径时更新 `internal/photos/labels/v1.calibration.json`；`--compare-with` 在两份缓存共有的图片上比较视觉 token 或输入尺寸。

## 标签阈值校准

AI 聚合的阈值按“宁缺毋滥”校准，场景标签的真值来自 COCO-Stuff：只取 `stuff_annotations_trainval2017.zip` 里的 `stuff_val2017.json`（22 MB），用 [remotezip](https://pypi.org/project/remotezip/) 按 HTTP Range 读出，不下载 1.1 GB 的整个压缩包，放在 `/mnt/d/A-NAS-datasets/coco-stuff/`：

```bash
$AI -m eval.calibrate --cache "/datasets/cache/<模型 ID>-e512" --min-precision 0.9 --min-label-precision 0.8 --min-recall 0.1 --write
```

校准集还可以加入 COCO 2017 train 的固定子集（2026-10-12 起）。验证集的 5,000 张让稀有标签和多数场景的照片太少，无法判断某组对不对；子集只用于校准，测试仍只用验证集的测试半。

[`eval.coco_train`](../../ai/eval/coco_train.py) 在开发机宿主上运行，需要 ijson 与 remotezip，不在评估镜像里。它按图片 ID 的哈希取 2 万张，把本机 `annotations_trainval2017.zip` 中的实例标注和远端 stuff 压缩包中的 `stuff_train2017.json` 以流式读取、压缩成每张图的类别与面积（约 11 MB），再逐张下载图片（约 3.1 GB）：

```bash
cd ai && ANAS_DATASETS=D:/A-NAS-datasets python -m eval.coco_train --count 20000
$AI -m eval.embed --model /models/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --tokens 70 --sets coco-train --no-texts
```

缓存里有子集的向量后，`eval.calibrate` 自动把它们并入校准半。v1 阈值还用 `--hold-back` 手动留下 8 组不启用（原因见[校准报告](../research/photo-ai-label-calibration.md#ai-聚合的严格校准)），写入的阈值文件记下这份名单：

```bash
$AI -m eval.calibrate --cache "/datasets/cache/<模型 ID>-e512" --min-precision 0.9 --min-label-precision 0.8 --min-recall 0.1 \
  --hold-back hot_dog,carrot,wine_glass,scissors,microwave,ball,bridge,sofa --report /datasets/report.json --write
```

## 分块向量与物体检测

分块向量（[M2 实施方案“AI 聚合”](../architecture/photo-ai.md#ai-聚合)）先为 COCO 测试半与 COCO-CN 测试集算每块的向量，再与整图向量比较；中途也可以运行 `eval.tile_search` 查看已算部分的结果：

```bash
$AI -m eval.embed --model /models/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --tokens 70 --tiles 2 --split test --sets coco,coco-cn
$AI -m eval.tile_search --cache "/datasets/cache/<模型 ID>-e512" --tiles "/datasets/cache/<模型 ID>-e512-tiles2"
```

物体检测与混合检索（[研究](../research/photo-ai-object-detection.md)）用另一个评估镜像：在 `anas-ai-dev:trixie` 上加装 ONNX Runtime（`FROM anas-ai-dev:trixie` 后 `RUN /opt/anas-ai/bin/pip install onnxruntime`，标记为 `anas-ai-dev-ort:trixie`）。D-FINE 的 ONNX 转换放在 `D:\A-NAS-models\dfine-obj365\dfine_m_obj365\`（`config.json`、`onnx/model.onnx` 与记下 revision 的 `hub.json`）。`eval.detect` 为 COCO、COCO-CN 测试集与 Open Images 缓存检测框，可中断续跑；`eval.hybrid_search` 把检测框与已缓存的照片向量组合比较，`--embedder` 第一次运行时编码中文类名，`--half calibration` 在 COCO 另一半上复核：

```bash
DET="docker run --rm -v $PWD:/src -w /src/ai -v /mnt/d/A-NAS-datasets:/datasets \
  -v /mnt/d/A-NAS-models/dfine-obj365:/models:ro -v /mnt/d/A-NAS-models/embeddinggemma-2-740m:/models-eg:ro \
  -v $HOME/.cache/anas-ai-xnnpack:/xnnpack -e PYTHONDONTWRITEBYTECODE=1 anas-ai-dev-ort:trixie /opt/anas-ai/bin/python"
$DET -m eval.detect --model /models/dfine_m_obj365 --threads 4 --sets coco,coco-cn,openimages
$DET -m eval.detect --model /models/dfine_m_obj365 --threads 4 --bench 30 --embedder /models-eg/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack
$DET -m eval.hybrid_search --vectors "/datasets/cache/<模型 ID>-e512" --detections "/datasets/cache/<检测模型 ID>" \
  --embedder /models-eg/embeddinggemma-2-740m.litertlm --weight-cache /xnnpack --report /datasets/runs/detector/report.json
```


## 关联

- [开发环境入口](LOCAL_ENVIRONMENT.md)
- [AI 评估代码](../../ai/eval/)
- [标签校准报告](../research/photo-ai-label-calibration.md)
- [AI 架构](../architecture/photo-ai.md)
