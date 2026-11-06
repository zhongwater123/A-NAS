import React, { useEffect, useSyncExternalStore } from "react";
import { FlatList, Pressable, Text, View } from "react-native";
import { router } from "expo-router";
import { useRuntime } from "../runtime";
import type { ChatController } from "../chat/controller";
import { Button, Loading, Notice, colors, styles } from "../ui/components";
export default function History() {
  const { chat } = useRuntime();
  return chat ? <HistoryList chat={chat} /> : <Loading />;
}
function HistoryList({ chat }: { chat: ChatController }) {
  const state = useSyncExternalStore(chat.subscribe, chat.snapshot);
  useEffect(() => {
    void chat.refreshHistory();
  }, [chat]);
  return (
    <FlatList
      style={styles.page}
      contentContainerStyle={styles.body}
      data={state.history}
      keyExtractor={(item) => item.id}
      ListHeaderComponent={
        <View style={{ gap: 16, marginBottom: 14 }}>
          <Button
            title="新对话"
            onPress={() => {
              void chat.open();
              router.back();
            }}
          />
          <Notice text={state.error} />
        </View>
      }
      ListEmptyComponent={
        <Text style={styles.muted}>还没有历史对话，开始聊聊吧。</Text>
      }
      renderItem={({ item }) => (
        <Pressable
          accessibilityRole="button"
          onPress={() => {
            void chat.open(item.id);
            router.back();
          }}
          style={{
            paddingVertical: 18,
            borderBottomWidth: 1,
            borderBottomColor: colors.line,
            gap: 8,
          }}
        >
          <Text numberOfLines={2} style={styles.text}>
            {item.title || "未命名对话"}
          </Text>
          <Text numberOfLines={2} style={styles.muted}>
            {item.preview}
          </Text>
        </Pressable>
      )}
    />
  );
}
