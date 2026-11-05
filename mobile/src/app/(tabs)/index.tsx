import React, { useRef, useState, useSyncExternalStore } from "react";
import {
  FlatList,
  KeyboardAvoidingView,
  Platform,
  Pressable,
  Text,
  TextInput,
  View,
} from "react-native";
import { ArrowUp, Square } from "lucide-react-native";
import { useRuntime } from "../../runtime";
import type { ChatController } from "../../chat/controller";
import { ApprovalCard } from "../../chat/ApprovalCard";
import {
  Avatar,
  Button,
  Loading,
  Notice,
  colors,
  styles,
} from "../../ui/components";
export default function Chat() {
  const { chat } = useRuntime();
  return chat ? <Conversation chat={chat} /> : <Loading />;
}
function Conversation({ chat }: { chat: ChatController }) {
  const state = useSyncExternalStore(chat.subscribe, chat.snapshot);
  const [draft, setDraft] = useState(""),
    [sending, setSending] = useState(false);
  const list = useRef<FlatList>(null);
  async function send() {
    if (sending) return;
    setSending(true);
    const text = draft;
    try {
      if (await chat.send(text)) setDraft((old) => (old === text ? "" : old));
    } finally {
      setSending(false);
    }
  }
  return (
    <KeyboardAvoidingView
      style={styles.page}
      behavior={Platform.OS === "ios" ? "padding" : undefined}
      keyboardVerticalOffset={96}
    >
      <View style={{ paddingHorizontal: 22, paddingBottom: 10 }}>
        <Text style={styles.muted}>
          {state.ready
            ? state.activity || "已连接 NAS 上的 VNAS"
            : "VNAS 尚未连接"}
        </Text>
      </View>
      <FlatList
        ref={list}
        data={state.messages}
        keyExtractor={(m) => m.id}
        keyboardShouldPersistTaps="handled"
        contentContainerStyle={{ padding: 22, gap: 22, flexGrow: 1 }}
        onContentSizeChange={() =>
          list.current?.scrollToEnd({ animated: false })
        }
        ListEmptyComponent={
          state.loading ? (
            <Loading text="正在读取对话…" />
          ) : (
            <View
              style={{
                flex: 1,
                justifyContent: "center",
                alignItems: "flex-start",
                gap: 16,
                paddingBottom: 60,
              }}
            >
              <Avatar size={60} />
              <Text style={styles.title}>有什么交给我？</Text>
              <Text style={styles.muted}>
                查找 NAS 里的资料，或接着聊上次的事。
              </Text>
              <View style={{ height: 8 }} />
              <Button
                title="帮我找一份文件"
                quiet
                onPress={() => setDraft("帮我找一份文件")}
              />
              <Button
                title="你能帮我做些什么？"
                quiet
                onPress={() => setDraft("你能帮我做些什么？")}
              />
            </View>
          )
        }
        renderItem={({ item }) => (
          <View
            style={{
              alignSelf: item.role === "user" ? "flex-end" : "flex-start",
              maxWidth: "94%",
              padding: item.role === "user" ? 15 : 0,
              borderRadius: 18,
              backgroundColor:
                item.role === "user" ? colors.soft : "transparent",
            }}
          >
            <Text selectable style={styles.text}>
              {item.text}
            </Text>
          </View>
        )}
        ListFooterComponent={
          <View style={{ gap: 16 }}>
            {state.approvals.map((a) => (
              <ApprovalCard key={a.requestId} approval={a} chat={chat} />
            ))}
            <Notice text={state.error} />
            {!state.ready && (
              <Button
                title="重新连接"
                quiet
                onPress={() => void chat.start()}
              />
            )}
          </View>
        }
      />
      <View
        style={{
          marginHorizontal: 18,
          marginBottom: 12,
          borderWidth: 1,
          borderColor: colors.line,
          borderRadius: 22,
          backgroundColor: "#FFF",
          paddingLeft: 14,
          paddingRight: 6,
          paddingVertical: 6,
          flexDirection: "row",
          alignItems: "flex-end",
          gap: 6,
        }}
      >
        <TextInput
          accessibilityLabel="发给 VNAS 的消息"
          placeholder="发消息给 VNAS"
          placeholderTextColor={colors.muted}
          multiline
          value={draft}
          onChangeText={setDraft}
          style={{
            flex: 1,
            minHeight: 44,
            maxHeight: 140,
            fontSize: 16,
            lineHeight: 23,
            paddingVertical: 11,
            color: colors.ink,
          }}
        />
        <Pressable
          accessibilityRole="button"
          accessibilityLabel={state.busy ? "停止回答" : "发送消息"}
          disabled={
            state.busy
              ? !state.ready
              : !state.ready || !draft.trim() || sending || state.loading
          }
          onPress={() => (state.busy ? void chat.stop() : void send())}
          style={{
            width: 44,
            height: 44,
            alignItems: "center",
            justifyContent: "center",
            borderRadius: 22,
            backgroundColor: colors.ink,
            opacity: state.ready ? 1 : 0.4,
          }}
        >
          {state.busy ? (
            <Square size={16} color="#FFF" />
          ) : (
            <ArrowUp size={22} color="#FFF" />
          )}
        </Pressable>
      </View>
    </KeyboardAvoidingView>
  );
}
