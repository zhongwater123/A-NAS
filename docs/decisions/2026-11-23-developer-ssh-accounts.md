# 实习生的 SSH 账号

状态：decided（2026-11-23，用户）
更新时间：2026-11-23

## 问题

用户要把实验 NAS 宿主机上的工作交给开发实习生，需要给他们 SSH 账号。已有的 `guoyi`（只读联调）与 `anas-dev`（暂存发布）各属一台开发机，不能共用；10 月底为 OCR 评测开的 `dev-ocr` 已不用。要定的是：实习生能做什么、账号怎么分、何时失效、sshd 是否为此收紧，以及评测用的环境怎么共享。

## 选项

- **用途**（可多选）：只读排查；看服务日志（加入 `systemd-journal`，能看全部系统日志，含成员账号名与文件路径）；跑模型或评测；暂存发布包。
- **账号形式**：每人一个 `dev-<缩写>`，登录记录对得上人、可单独撤销；或共用一个账号放多把钥匙。
- **到期**：用 `usermod -e` 设到期日，到期后 PAM 拒绝登录；或不设，人走时手动撤销。
- **sshd**：为开发者组加一段 `Match Group`，禁用密码登录与转发；或不改 sshd，只靠账号不设密码。
- **共享评测环境**：把 `dev-ocr` 改成大家共用的登录账号；或每人用自己的账号登录，通过一个组共享一个目录。共用登录对不上人，任一使用者都能增删别人的钥匙，也能互删文件。

## 决定

用户 2026-11-23 选定：

- 用途：只读排查、看服务日志，以及共享的模型与评测环境。用户原话：“我想是否能配置一个共享的模型或评测账户，或者把dev-ocr转为共享的账户？”我建议不共用登录，改为组 `anas-eval` 加共享目录 `/srv/eval`，用户回复“开始做”。暂存发布包不在用途内。
- 每人一个账号。
- 不设到期。
- 为开发者组收紧 sshd。
- `dev-ocr`：“是之前测试OCR用的，已经废弃，可以搬走或是销毁”。盘点后用户选“搬模型数据集，其余删”。2026-11-23 用一次性 root 脚本执行：
  - 三个模型（qwen3.5-4b、gemma-4-e4b、gemma-4-e4b-qat，共约 15 GB）搬到 `/srv/eval/models/`；
  - CC-OCR 评测集搬到 `/srv/eval/datasets/cc-ocr`；
  - 评测脚本与运行记录搬到 `/srv/eval/archive/ocr-eval-2026-10-29/`；
  - 删除账号与其余 2.2 GB；
  - root 名下唯一的钥匙 `claude-ocr-eval@OCR_test` 与 dev-ocr 的是同一把，已停用（改名为 `/root/.ssh/authorized_keys.disabled-20261123`）。

## 关联

- 运行手册：[为开发者开、撤实验 NAS 的 SSH 账号](../runbooks/manage-developer-ssh-accounts.md)，其中写了实施时的具体做法：钥匙放在 root 名下、UID 区间、每个账号的资源上限
- 脚本：[`scripts/nas-dev-account.sh`](../../scripts/nas-dev-account.sh)
- 开发者一侧：[用开发者账号连接实验 NAS](../development/experimental-nas-access.md)
