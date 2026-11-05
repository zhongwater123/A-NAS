import React, { useState } from "react";
import {
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  Text,
  View,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import Constants from "expo-constants";
import { useRuntime } from "../runtime";
import { errorText } from "../nas/client";
import { Avatar, Button, Field, Notice, styles } from "../ui/components";
export default function Login() {
  const runtime = useRuntime();
  const [address, setAddress] = useState(
    runtime.nas.origin ||
      (Constants.expoConfig?.extra?.lanHosts?.[0]
        ? `http://${Constants.expoConfig.extra.lanHosts[0]}`
        : ""),
  );
  const [user, setUser] = useState(""),
    [password, setPassword] = useState(""),
    [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  async function submit() {
    setBusy(true);
    setError("");
    try {
      await runtime.login(address, user, password);
      setPassword("");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <SafeAreaView style={styles.page}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        <ScrollView
          keyboardShouldPersistTaps="handled"
          contentContainerStyle={[styles.body, { paddingTop: 44, gap: 26 }]}
        >
          <Avatar size={66} />
          <View style={{ gap: 10 }}>
            <Text style={styles.title}>连接你的 NAS</Text>
            <Text style={styles.muted}>
              与家中的 VNAS 聊天，让资料在手机和 NAS 之间自由往来。
            </Text>
          </View>
          <Field
            label="NAS 地址"
            placeholder="http://192.168.1.10"
            value={address}
            onChangeText={setAddress}
            autoCapitalize="none"
            autoCorrect={false}
            keyboardType="url"
            editable={!busy}
          />
          <Field
            label="账号"
            placeholder="NAS 上的用户名"
            value={user}
            onChangeText={setUser}
            autoCapitalize="none"
            autoCorrect={false}
            autoComplete="username"
            editable={!busy}
          />
          <Field
            label="密码"
            placeholder="NAS 登录密码"
            value={password}
            onChangeText={setPassword}
            secureTextEntry
            autoComplete="current-password"
            editable={!busy}
            onSubmitEditing={() => {
              if (!busy) void submit();
            }}
          />
          <Notice text={error || runtime.error} />
          <Button
            title={busy ? "正在连接…" : "连接 NAS"}
            disabled={busy || !address.trim() || !user.trim() || !password}
            onPress={() => void submit()}
          />
          <Text style={styles.muted}>
            手机与 NAS 需要在同一局域网。HTTP 连接仅用于你信任的网络。
          </Text>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}
