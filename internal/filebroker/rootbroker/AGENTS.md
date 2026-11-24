# 文件代理的 root 一侧（internal/filebroker/rootbroker/）

本目录在 Host Agent 中以 root 运行：为每个成员启动以其身份运行的文件进程，并执行助手的“提交更改”。改动前读 [internal/hostops/AGENTS.md](../../hostops/AGENTS.md)，尤其是“成员控制的路径都是边界”一条。
