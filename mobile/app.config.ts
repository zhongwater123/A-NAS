import type { ExpoConfig } from "expo/config";
import ipaddr from "ipaddr.js";
const hosts = (process.env.ANAS_LAN_HOSTS ?? "")
  .split(",")
  .map((v) => v.trim())
  .filter(Boolean);
for (const host of hosts) {
  if (
    !ipaddr.isValid(host) ||
    !["private", "loopback", "linkLocal", "uniqueLocal"].includes(
      ipaddr.process(host).range(),
    )
  )
    throw new Error("ANAS_LAN_HOSTS must contain local IP addresses only");
}
const config: ExpoConfig = {
  name: "A-NAS",
  slug: "a-nas-mobile",
  version: "0.1.0",
  orientation: "portrait",
  userInterfaceStyle: "light",
  ios: {
    bundleIdentifier: "com.anas.mobile",
    supportsTablet: false,
    infoPlist: {
      NSLocalNetworkUsageDescription:
        "连接同一局域网中的 NAS，与 VNAS 聊天并传输你的文件。",
      NSAppTransportSecurity: {
        NSExceptionDomains: Object.fromEntries(
          hosts.map((host) => [
            host,
            {
              NSExceptionAllowsInsecureHTTPLoads: true,
              NSIncludesSubdomains: false,
            },
          ]),
        ),
      },
      UIFileSharingEnabled: false,
    },
  },
  android: {
    package: "com.anas.mobile",
    permissions: ["INTERNET", "ACCESS_NETWORK_STATE"],
  },
  plugins: [
    ["expo-build-properties", { ios: { deploymentTarget: "17.0" } }],
    "expo-router",
    "expo-secure-store",
    "expo-document-picker",
    "expo-sharing",
    ["./plugins/withLanHosts.js", { hosts }],
  ],
  extra: { lanHosts: hosts },
};
export default config;
