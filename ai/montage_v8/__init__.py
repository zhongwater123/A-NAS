"""视频剪辑第八版：第六版流水线按分层重构，业务层跨开发机、x86 NAS 与 RK3588（docs/specs/video-montage-architecture.md）。

domain/    数据和规则，不做输入输出
ports.py   外部能力的接口
adapters/  各平台的实现（FFmpeg、视觉模型、规划模型、音乐、存储）
profiles/  平台配置和素材设定
pipeline.py 编排；__main__.py 命令行；golden.py 和第六版比对；doctor.py 平台自检
"""
