const {
  withAndroidManifest,
  withDangerousMod,
} = require("expo/config-plugins");
const fs = require("node:fs/promises");
const path = require("node:path");
module.exports = function withLanHosts(config, { hosts = [] }) {
  config = withAndroidManifest(config, (cfg) => {
    const app = cfg.modResults.manifest.application[0];
    app.$["android:networkSecurityConfig"] = "@xml/anas_network_security";
    app.$["android:usesCleartextTraffic"] = "false";
    return cfg;
  });
  return withDangerousMod(config, [
    "android",
    async (cfg) => {
      const dir = path.join(
        cfg.modRequest.platformProjectRoot,
        "app/src/main/res/xml",
      );
      await fs.mkdir(dir, { recursive: true });
      const domains = hosts
        .map((host) => `<domain includeSubdomains="false">${host}</domain>`)
        .join("");
      await fs.writeFile(
        path.join(dir, "anas_network_security.xml"),
        `<?xml version="1.0" encoding="utf-8"?><network-security-config><base-config cleartextTrafficPermitted="false"/>${domains ? `<domain-config cleartextTrafficPermitted="true">${domains}</domain-config>` : ""}</network-security-config>`,
      );
      return cfg;
    },
  ]);
};
