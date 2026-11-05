import React, { useState } from "react";
import { ScrollView, Text, View } from "react-native";
import { useRuntime } from "../runtime";
import { Button, Notice, styles } from "../ui/components";
import { errorText } from "../nas/client";
export default function Settings() {
  const { nas, session, logout } = useRuntime();
  const [busy, setBusy] = useState(false),
    [error, setError] = useState("");
  return (
    <ScrollView contentContainerStyle={styles.body}>
      <View style={styles.box}>
        <Text style={styles.label}>当前 NAS</Text>
        <Text selectable style={styles.text}>
          {nas.origin}
        </Text>
        <Text style={styles.label}>登录账号</Text>
        <Text style={styles.text}>{session?.user.username}</Text>
      </View>
      <Text style={styles.muted}>
        切换 NAS 或账号前，正在进行的传输会暂停。本机传输记录按 NAS
        和账号分别保存。
      </Text>
      <Notice text={error} />
      <Button
        title={busy ? "正在退出…" : "退出登录 / 切换 NAS"}
        quiet
        disabled={busy}
        onPress={() => {
          setBusy(true);
          void logout()
            .catch((e) => setError(errorText(e)))
            .finally(() => setBusy(false));
        }}
      />
    </ScrollView>
  );
}
