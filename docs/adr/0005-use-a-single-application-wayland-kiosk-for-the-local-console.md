# Use a single-application Wayland kiosk for the local console

2026-11-10 修订计划：保留普通 Kiosk；产品 Kiosk 页面和回环连接不作为可信恢复或销毁确认入口。独立维护入口在[改造方案 P1–P2](../architecture/compromised-product-data-protection.md#改造顺序与交付门禁)实施；见 [ADR 0036](0036-protect-recovery-from-a-compromised-product-service.md)。以下为既有实现决定。

The Experimental NAS is an ITX computer with its own display and input devices, not a headless server. A-NAS will present the same embedded Web desktop locally through an unprivileged Cage/Chromium Wayland kiosk instead of installing a general-purpose desktop environment; this gives the device an appliance-like console while keeping Product Service and Host Agent permissions unchanged and retaining the SSH tunnel as an optional remote development path.
