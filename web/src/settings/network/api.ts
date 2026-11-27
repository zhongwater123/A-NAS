import { call, client } from "../../api/client";
import type { PhoneHotspotSettingsChange } from "../../api.generated";

const path = "/api/v1/network/phone-hotspot";

export function readPhoneHotspot(signal: AbortSignal) {
  return call(client.GET(path, { signal }));
}

export function setPhoneHotspot(enabled: boolean) {
  return call(client.PUT(path, { body: { enabled } }));
}

export function savePhoneHotspotSettings(body: PhoneHotspotSettingsChange) {
  return call(client.PUT(`${path}/settings`, { body }));
}
