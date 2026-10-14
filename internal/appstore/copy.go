package appstore

import "strings"

// The vendored manifests stay as upstream wrote them (catalog/NOTICE.md), so
// text that is wrong for an A-NAS is corrected here, after parsing.

// appNotes replaces the first-use notes of apps whose upstream notes walk
// through CasaOS settings or /DATA paths that do not exist on an A-NAS. The
// upstream notes stay available as UpstreamNotes. On an A-NAS /DATA/AppData/
// <app> is the app's own data folder and every other /DATA path is the
// Shared space (render.go).
var appNotes = map[string]string{
	"bentopdf": `安装后点“打开”即可使用，不需要账号；PDF 都在浏览器里处理，不会上传到 NAS。

转换 Office 文件（Word、Excel、PowerPoint）需要浏览器的安全上下文（HTTPS）。A-NAS 的局域网入口是 HTTP，所以从局域网打开时这几项转换不可用，其他工具不受影响。`,

	"deluge": `默认密码：` + "`deluge`" + `，登录后在“Preferences → Interface”中修改。

Deluge 里看到的 ` + "`/DATA`" + ` 就是 A-NAS 的**共享空间**，下载的文件所有成员都能在文件管理中看到：

| Deluge 中的路径 | A-NAS 中的位置 |
| --- | --- |
| ` + "`/DATA/Downloads`" + ` | 共享空间 › Downloads |
| ` + "`/DATA/Media`" + ` | 共享空间 › Media |`,

	"etherpad": `管理后台在 Etherpad 网址后加 ` + "`/admin`" + `，账号 ` + "`admin`" + `，密码由 A-NAS 在安装时随机生成，见本页的**登录信息**。

Pad 保存在应用数据中（内置的 dirty 数据库），卸载后保留，适合个人或小团队使用。`,

	"immich": `照片和视频保存在**共享空间 › Gallery › immich**，数据库和识别模型保存在应用数据中，安装计划的“使用文件夹”列出了全部位置。

1. 安装完成后点“打开”，第一个注册的账号就是 Immich 的管理员。
2. 在手机上安装 Immich App，服务器地址填 ` + "`http://<NAS 地址>:2283`" + `，即可备份手机相册。

Immich 仍在快速开发，可能出现错误或不兼容的升级。不要把它当作照片和视频的唯一存放处，请另外备份。`,

	"openlist": `账号 ` + "`admin`" + `，密码由 A-NAS 在安装时随机生成，见本页的**登录信息**。

OpenList 的设置和添加的网盘保存在应用数据中，卸载后保留。它只能访问自己的应用数据，看不到 NAS 上的共享空间和个人空间，适合用来挂载网盘。`,

	"photoprism": `账号 ` + "`admin`" + `，密码由 A-NAS 在安装时随机生成，见本页的**登录信息**。

PhotoPrism 整理的原图在**共享空间 › Gallery**，所有成员都能在文件管理中看到；索引和缩略图保存在应用数据中，卸载后保留。`,

	"pyload": `默认账号 ` + "`pyload`" + `、密码 ` + "`pyload`" + `，登录后请修改。

下载的文件保存在**共享空间 › Downloads**，在 pyLoad 里这个文件夹叫 ` + "`/downloads`" + `。`,

	"qbittorrent": `用户名 ` + "`admin`" + `；首次启动的临时密码写在容器日志里：在 **Docker** 中打开 qbittorrent 的日志即可看到。登录后在“设置 → Web UI”中改成自己的密码。

qBittorrent 里看到的 ` + "`/DATA`" + ` 就是 A-NAS 的**共享空间**，下载的文件所有成员都能在文件管理中看到：

| qBittorrent 中的路径 | A-NAS 中的位置 |
| --- | --- |
| ` + "`/DATA/Downloads`" + ` | 共享空间 › Downloads |
| ` + "`/DATA/Media`" + ` | 共享空间 › Media |`,

	"siyuan-note": `打开后输入访问授权码进入，授权码由 A-NAS 在安装时随机生成，见本页的**登录信息**。知道授权码的人都能读写全部笔记，请不要外传。

笔记工作区保存在应用数据中，卸载后保留。`,

	"transmission": `账号和密码见本页的**登录信息**，密码由 A-NAS 在安装时随机生成。

Transmission 里看到的 ` + "`/downloads`" + ` 就是**共享空间 › Downloads**，下载的文件所有成员都能在文件管理中看到；把种子文件放进**共享空间 › Downloads › watch**会自动开始下载。`,
}

// withoutVendorPitch drops the paragraphs of a description, and whole
// sections under such a heading, that sell the app as running on Zima
// hardware: marketing for another product, with claims such as unlimited
// storage that are untrue of an A-NAS.
func withoutVendorPitch(text string) string {
	blocks := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n\n")
	kept := make([]string, 0, len(blocks))
	skipBelow := 0
	for _, block := range blocks {
		trimmed := strings.TrimSpace(block)
		level := headingLevel(trimmed)
		if skipBelow > 0 {
			if level == 0 || level > skipBelow {
				continue
			}
			skipBelow = 0
		}
		if strings.Contains(strings.ToLower(trimmed), "zima") {
			skipBelow = level
			continue
		}
		kept = append(kept, block)
	}
	return strings.TrimSpace(strings.Join(kept, "\n\n"))
}

// headingLevel is the level of a Markdown ATX heading, or 0.
func headingLevel(block string) int {
	level := 0
	for level < len(block) && block[level] == '#' {
		level++
	}
	if level == 0 || level > 6 || level == len(block) || block[level] != ' ' {
		return 0
	}
	return level
}
