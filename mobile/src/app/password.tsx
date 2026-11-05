import React, { useState } from "react";
import { ScrollView, Text } from "react-native";
import { useRuntime } from "../runtime";
import { unwrap, errorText } from "../nas/client";
import { Button, Field, Notice, styles } from "../ui/components";
export default function Password() {
  const { nas, refreshSession, logout } = useRuntime();
  const [old, setOld] = useState(""),
    [next, setNext] = useState(""),
    [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function save() {
    setBusy(true);
    try {
      await unwrap(
        nas.api.POST("/api/v1/session/password", {
          params: { header: { "X-CSRF-Token": nas.csrf } },
          body: { currentPassword: old, newPassword: next },
        }),
      );
      setOld("");
      setNext("");
      await refreshSession();
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <ScrollView contentContainerStyle={styles.body}>
      <Text style={styles.text}>此账号需要更新密码后才能继续使用。</Text>
      <Field
        label="当前密码"
        secureTextEntry
        value={old}
        onChangeText={setOld}
      />
      <Field
        label="新密码（12–64 个字符）"
        secureTextEntry
        value={next}
        onChangeText={setNext}
      />
      <Notice text={error} />
      <Button
        title="保存密码"
        disabled={busy || !old || !next}
        onPress={() => void save()}
      />
      <Button title="退出登录" quiet onPress={() => void logout()} />
    </ScrollView>
  );
}
