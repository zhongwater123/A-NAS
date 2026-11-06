import React from "react";
import { Stack } from "expo-router";
import { StatusBar } from "expo-status-bar";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { RuntimeProvider, useRuntime } from "../runtime";
import { Loading, colors } from "../ui/components";

function Routes() {
  const { session, booting } = useRuntime();
  if (booting) return <Loading text="正在连接 NAS…" />;
  return (
    <Stack
      screenOptions={{
        headerStyle: { backgroundColor: colors.paper },
        headerTintColor: colors.ink,
        contentStyle: { backgroundColor: colors.paper },
        headerShadowVisible: false,
      }}
    >
      <Stack.Protected guard={!session}>
        <Stack.Screen name="login" options={{ headerShown: false }} />
      </Stack.Protected>
      <Stack.Protected guard={Boolean(session?.user.mustChangePassword)}>
        <Stack.Screen name="password" options={{ title: "更新密码" }} />
      </Stack.Protected>
      <Stack.Protected
        guard={Boolean(session && !session.user.mustChangePassword)}
      >
        <Stack.Screen name="(tabs)" options={{ headerShown: false }} />
        <Stack.Screen name="history" options={{ title: "历史对话" }} />
        <Stack.Screen name="settings" options={{ title: "连接与账号" }} />
        <Stack.Screen
          name="files"
          options={{ title: "NAS 文件", presentation: "modal" }}
        />
      </Stack.Protected>
    </Stack>
  );
}
export default function Layout() {
  return (
    <SafeAreaProvider>
      <RuntimeProvider>
        <StatusBar style="dark" />
        <Routes />
      </RuntimeProvider>
    </SafeAreaProvider>
  );
}
