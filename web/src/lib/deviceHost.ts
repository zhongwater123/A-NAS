// namesDeviceByAddress mirrors the server's own test (localorigin.DeviceHost):
// localhost on the local console, an IP address on the LAN. Installed apps'
// own ports and agents' calls to 共享工作区 work only at such an address
// (against DNS rebinding).
export function namesDeviceByAddress(hostname: string): boolean {
  if (hostname === "localhost" || hostname.startsWith("[")) return true;
  return /^\d{1,3}(\.\d{1,3}){3}$/.test(hostname);
}
