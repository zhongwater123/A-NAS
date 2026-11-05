import { fetch as expoFetch } from "expo/fetch";
import * as SecureStore from "expo-secure-store";
import Constants from "expo-constants";
import { NasClient } from "./client";
const key = "anas.mobile.session.v1";
export function createNasClient() {
  return new NasClient(
    expoFetch as typeof fetch,
    {
      read: () => SecureStore.getItemAsync(key),
      write: (value) =>
        SecureStore.setItemAsync(key, value, {
          keychainAccessible: SecureStore.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
        }),
      clear: () => SecureStore.deleteItemAsync(key),
    },
    Constants.expoConfig?.extra?.lanHosts ?? [],
  );
}
