import { describe, expect, it } from "vitest";

import { appURL } from "./model";

const at = (href: string) => new URL(href) as unknown as Location;
const app = { webPort: 8096, scheme: "http", path: "/web/" };

describe("appURL", () => {
  it("opens the app on the address the desktop was opened from", () => {
    expect(appURL(app, at("http://172.18.45.48/"))).toBe("http://172.18.45.48:8096/web/");
    expect(appURL(app, at("http://localhost:8080/?local-console=1"))).toBe("http://localhost:8096/web/");
    expect(appURL(app, at("http://[fd00::48]/"))).toBe("http://[fd00::48]:8096/web/");
  });

  it("has no address through the internet entry, where the app's port cannot be reached", () => {
    expect(appURL(app, at("https://nas.example.com/"))).toBeUndefined();
  });

  it("has no address for an app without a web interface", () => {
    expect(appURL({ ...app, webPort: 0 }, at("http://172.18.45.48/"))).toBeUndefined();
  });
});
