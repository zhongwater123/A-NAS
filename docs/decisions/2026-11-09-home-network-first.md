# 第一版先做家庭局域网内访问

状态：decided（2026-11-09，用户）
更新时间：2026-11-23

## 问题

[ADR 0032](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md) 的互联网入口要等 IT 把公网端口映射到机房的边缘服务器，还要域名与备案。2026-11-09 用户给 NAS 装了 USB 无线网卡，准备让它接入家庭网络。要定的是：第一版的用户从哪里访问 NAS，手机与 NAS 之间用什么传输。

## 选项

- 访问范围：
  - **家庭局域网内**：NAS 接入家里的 Wi-Fi 或宽带，同一网络里的手机用 App 访问；不开放互联网。
  - **互联网**：按 ADR 0032 经边缘服务器转发，在外面也能用。
- 传输：
  - **HTTPS 加扫码配对**：NAS 自带根证书作为身份，App 扫“设置”里的二维码记住根证书指纹。
  - **明文 HTTP**：只在可信局域网内可接受（[ADR 0012](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)），手机 App 无法确认连的是自己的 NAS。

## 决定

2026-11-09，用户重新定义了第一版：产品面向私人家庭，NAS 接入家里的 Wi-Fi 或宽带，同一网络里的手机用 App 访问；第一版不做互联网访问。传输选了推荐的“HTTPS 加扫码配对”。手机 App 由他人开发，A-NAS 这边负责 NAS 侧：局域网发现、配对、接口与对接文档。

由此：

- ADR 0032 的实现留在主干，入口保持关闭；NAS 离开公司网络后，机房的边缘服务器也用不上了。以后做互联网访问，换成云端中继，设计不变（NAS 主动连出，只换中继端点）。
- NAS 侧的工作还没开始，等用户说“开始做”。已知要做的：用 NetworkManager 取代 ifupdown（无线与有线并存，有线优先）；smbd 监听全部物理网口；设备主机名接受 `.local`；装 `firmware-mediatek`（无线网卡 MT7612U 需要）。

## 关联

- [ADR 0032：从互联网开放 Web 桌面](../adr/0032-reach-the-web-desktop-from-the-internet-through-an-edge-relay.md)、[互联网访问规格](../specs/internet-access.md)
- [ADR 0012：局域网内经 Caddy 以 HTTP 提供 Web 桌面](../adr/0012-serve-the-web-desktop-on-the-lan-over-http-through-caddy.md)
