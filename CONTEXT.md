# A-NAS

A-NAS 是可信家庭存储与受控 AI 能力共同组成的产品上下文。本文件只定义项目专有语言；产品历史、实现和操作步骤由文档索引路由到其他事实源。

## Language

**产品服务（Product Service）**：
承载产品 API、领域模块和编排逻辑的非特权服务。
_Avoid_：后端、管理面、Core

**Host Agent**：
执行少量宿主机特权操作的独立服务，只接受经过校验的高层意图。
_Avoid_：root 服务、Shell Agent

**Adapter**：
把产品定义的能力契约连接到某个运行环境或外部系统的实现。
_Avoid_：工具类、胶水层

**Fake Adapter**：
在开发和测试中以确定性状态模拟系统能力的 Adapter。
_Avoid_：Mock 服务、假后端

**Linux Adapter**：
通过 Debian/Linux 的稳定接口实现真实宿主机能力的 Adapter。
_Avoid_：生产 Adapter、真实后端

**执行计划（Execution Plan）**：
对宿主机变更进行校验后生成、供用户确认并随后执行的明确操作描述。
_Avoid_：Shell 脚本、命令列表

**稳定资源 ID（Stable Resource ID）**：
不随文件路径、挂载点或临时设备名变化的资源身份，用于授权、引用和清理。
_Avoid_：路径 ID、`/dev/sdX`

**Policy**：
文件访问、共享、搜索和 AI 检索共同使用的授权规则来源。
_Avoid_：AI 权限、搜索 ACL

**派生数据（Derived Data）**：
可从原文件重新生成并可独立删除的数据，例如缩略图、文本块、Embedding 和缓存。
_Avoid_：用户数据、原文件

**AI Provider**：
按照统一能力契约提供 OCR、ASR、Embedding、理解或生成能力的云端或本地实现。
_Avoid_：模型 SDK、AI 后端

**实验 NAS（Experimental NAS）**：
用于真实硬件、磁盘和系统集成测试且允许重装的裸机，不是源码或用户数据的唯一副本。
_Avoid_：生产 NAS、开发主机
