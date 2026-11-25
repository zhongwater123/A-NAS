import { QueryClient, QueryClientProvider, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Switch } from "radix-ui";
import { useState } from "react";

import { call, client } from "../api/client";
import { errorText } from "../api/errorMessages";
import type { PhoneHotspotStatus } from "../api.generated";
import { Banner, Group, PageHeader, Pill, Row, type Tone } from "./parts";
import styles from "./settings.module.css";

const path = "/api/v1/network/phone-hotspot";
const queryKey = ["settings", "phone-hotspot"];

const labels: Record<PhoneHotspotStatus["state"], { text: string; tone: Tone }> = {
  off: { text: "已关闭", tone: "neutral" },
  starting: { text: "正在开启", tone: "accent" },
  on: { text: "已开启", tone: "success" },
  stopping: { text: "正在关闭", tone: "accent" },
  failed: { text: "启动或运行失败", tone: "danger" },
  unavailable: { text: "不可用", tone: "neutral" },
};

const reasons: Record<NonNullable<PhoneHotspotStatus["reason"]>, string> = {
  dependencies_missing: "此设备需要安装 A-NAS 热点支持包。",
  no_wired_uplink: "请先确认 NAS 已通过网线连接网络。",
  no_idle_radio: "请插入一块空闲且支持热点的 MT7612U USB 无线网卡。",
  ap_failed: "无线网卡无法开启或维持热点。请检查网卡后重试。",
  dhcp_failed: "热点无法为手机分配 IP 地址。请重试。",
  web_unavailable: "热点暂时无法访问 A-NAS 网页。请检查有线端的网页是否正常。",
  tls_unavailable: "请先配置手机可验证的热点 HTTPS 证书和域名。",
  isolation_failed: "无线网卡无法进入热点的独立网络，请检查网卡与驱动。",
  cleanup_failed: "上一次热点关闭时仍有资源待清理。请检查网卡后重试。",
  service_failed: "热点服务遇到系统错误。请重试。",
};

export function NetworkSection() {
  const [client] = useState(() => new QueryClient({ defaultOptions: { queries: { retry: 1 } } }));
  return (
    <QueryClientProvider client={client}>
      <PhoneHotspotSettings />
    </QueryClientProvider>
  );
}

function PhoneHotspotSettings() {
  const cache = useQueryClient();
  const [revealedPassword, revealPassword] = useState<string>();
  const status = useQuery({
    queryKey,
    queryFn: ({ signal }) => call(client.GET(path, { signal })),
    // The password is used only on this page.
    gcTime: 0,
    refetchInterval: ({ state: { data } }) => (data?.state === "starting" || data?.state === "stopping" ? 750 : 5000),
  });
  const change = useMutation({
    mutationFn: (enabled: boolean) => call(client.PUT(path, { body: { enabled } })),
    onMutate: () => {
      revealPassword(undefined);
      return cache.cancelQueries({ queryKey });
    },
    onSuccess: async (next) => {
      await cache.cancelQueries({ queryKey });
      cache.setQueryData(queryKey, next);
    },
    onError: () => cache.invalidateQueries({ queryKey }),
  });
  const phase = status.data?.state;
  const reason = status.data?.reason;
  const connection = phase === "on" && !change.isPending ? status.data?.connection : undefined;
  const error = change.error ?? status.error;
  const busy = change.isPending || phase === "starting" || phase === "stopping";
  const reveal = connection != null && revealedPassword === connection.password;

  return (
    <>
      <PageHeader title="网络" />
      {error && <Banner tone="error">{errorText(error)}</Banner>}
      <Group anchor="phone-hotspot">
        <Row
          label={
            <>
              手机直连热点
              <span aria-live="polite">
                {phase && <Pill tone={labels[phase].tone}>{labels[phase].text}</Pill>}
                {status.isPending && "正在读取"}
              </span>
            </>
          }
          detail="让附近的手机通过 WebOS 上传文件。"
        >
          <Switch.Root
            className={styles.setSwitch}
            aria-label="手机直连热点"
            checked={phase === "on" || phase === "starting"}
            disabled={!phase || phase === "unavailable" || busy}
            onCheckedChange={(enabled) => change.mutate(enabled)}
          >
            <Switch.Thumb />
          </Switch.Root>
        </Row>
        {reason && <Row label="热点状态" detail={reasons[reason]} />}
        {connection && (
          <>
            <Row label="Wi-Fi 名称" value={<code className={styles.networkValue}>{connection.ssid}</code>} />
            <Row
              label="密码"
              detail={reveal ? <code className={styles.networkValue}>{connection.password}</code> : undefined}
            >
              <button
                type="button"
                className={styles.setBtn}
                onClick={() => revealPassword(reveal ? undefined : connection.password)}
              >
                {reveal ? "隐藏密码" : "显示密码"}
              </button>
            </Row>
            <Row label="WebOS 地址" value={<code className={styles.networkValue}>{connection.url}</code>} />
            <Row
              label="用手机连接"
              detail="加入热点后，在手机浏览器打开上方地址。如果手机提示没有互联网，请保持连接。"
            />
          </>
        )}
      </Group>
    </>
  );
}
