import React, { useEffect, useState } from "react";
import { FlatList, Pressable, Text, View } from "react-native";
import { router, useLocalSearchParams } from "expo-router";
import * as DocumentPicker from "expo-document-picker";
import { Folder, FileText, ChevronRight, Check } from "lucide-react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { useRuntime } from "../runtime";
import { errorText, unwrap, type Space, type FileEntry } from "../nas/client";
import {
  Button,
  Loading,
  Notice,
  bytes,
  colors,
  styles,
} from "../ui/components";
export default function Files() {
  const { mode } = useLocalSearchParams<{ mode: string }>();
  const uploading = mode === "upload";
  const { nas, transfers } = useRuntime();
  const insets = useSafeAreaInsets();
  const [spaces, setSpaces] = useState<Space[]>([]),
    [space, setSpace] = useState<Space>(),
    [folders, setFolders] = useState<FileEntry[]>([]),
    [entries, setEntries] = useState<FileEntry[]>([]),
    [chosen, setChosen] = useState<FileEntry>();
  const [loading, setLoading] = useState(true),
    [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [retry, setRetry] = useState(0);
  const parentId = folders.at(-1)?.id;
  function prepare() {
    setLoading(true);
    setError("");
    setChosen(undefined);
  }
  useEffect(() => {
    let current = true;
    const request = space
      ? unwrap(
          nas.api.GET("/api/v1/spaces/{spaceId}/entries", {
            params: { path: { spaceId: space.id }, query: { parentId } },
          }),
        ).then((result) => {
          if (current) setEntries(result.items);
        })
      : unwrap(nas.api.GET("/api/v1/spaces")).then((result) => {
          if (current) setSpaces(result.items);
        });
    void request
      .catch((e) => {
        if (current) setError(errorText(e));
      })
      .finally(() => {
        if (current) setLoading(false);
      });
    return () => {
      current = false;
    };
  }, [nas, space, parentId, retry]);
  async function submit() {
    if (!transfers || !space) return;
    setBusy(true);
    setError("");
    try {
      if (uploading) {
        const picked = await DocumentPicker.getDocumentAsync({
          multiple: true,
          copyToCacheDirectory: true,
        });
        if (picked.canceled) return;
        await transfers.upload(picked.assets, space.id, parentId);
      } else if (chosen) await transfers.download(chosen);
      else return;
      router.dismiss();
      router.navigate("/transfers");
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <View style={styles.page}>
      <View style={{ paddingHorizontal: 22, paddingVertical: 12, gap: 12 }}>
        <Text style={styles.muted}>
          {uploading
            ? "选择 NAS 上的存储位置，再选择手机文件。"
            : "选择要下载到手机的文件。"}
        </Text>
        {space && (
          <Button
            quiet
            title={
              folders.length
                ? `返回上一级 · ${folders.at(-1)?.name}`
                : "返回空间列表"
            }
            onPress={() => {
              prepare();
              if (folders.length) setFolders((old) => old.slice(0, -1));
              else setSpace(undefined);
            }}
          />
        )}
        <Text numberOfLines={2} style={styles.label}>
          {space
            ? [space.name, ...folders.map((f) => f.name)].join(" / ")
            : "我的空间"}
        </Text>
        <Notice text={error} />
        {error && (
          <Button
            title="重新载入"
            quiet
            onPress={() => {
              prepare();
              setRetry((n) => n + 1);
            }}
          />
        )}
      </View>
      {loading ? (
        <Loading />
      ) : !space ? (
        <FlatList
          data={spaces}
          keyExtractor={(s) => s.id}
          renderItem={({ item }) => (
            <Pressable
              accessibilityRole="button"
              onPress={() => {
                prepare();
                setSpace(item);
                setFolders([]);
              }}
              style={[styles.row, { padding: 22 }]}
            >
              <Folder color={colors.ink} />
              <Text style={[styles.text, { flex: 1 }]}>{item.name}</Text>
              <ChevronRight color={colors.muted} />
            </Pressable>
          )}
        />
      ) : (
        <FlatList
          data={
            uploading ? entries.filter((e) => e.kind === "directory") : entries
          }
          keyExtractor={(e) => e.id}
          ListEmptyComponent={
            <Text style={{ ...styles.muted, padding: 22 }}>
              {uploading
                ? "这里没有子文件夹，可以直接上传到当前位置。"
                : "这个文件夹还没有文件。"}
            </Text>
          }
          renderItem={({ item }) => (
            <Pressable
              accessibilityRole="button"
              onPress={() => {
                if (item.kind === "directory") {
                  prepare();
                  setFolders((old) => [...old, item]);
                } else setChosen(item);
              }}
              style={[
                styles.row,
                {
                  paddingHorizontal: 22,
                  paddingVertical: 16,
                  backgroundColor:
                    chosen?.id === item.id ? colors.soft : "transparent",
                },
              ]}
            >
              {item.kind === "directory" ? (
                <Folder color={colors.muted} />
              ) : (
                <FileText color={colors.muted} />
              )}
              <View style={{ flex: 1, gap: 4 }}>
                <Text numberOfLines={2} style={styles.text}>
                  {item.name}
                </Text>
                {item.kind === "file" && (
                  <Text style={styles.muted}>{bytes(item.sizeBytes)}</Text>
                )}
              </View>
              {item.kind === "directory" ? (
                <ChevronRight color={colors.muted} />
              ) : chosen?.id === item.id ? (
                <Check color={colors.ink} />
              ) : null}
            </Pressable>
          )}
        />
      )}
      <View
        style={{
          padding: 22,
          paddingBottom: Math.max(22, insets.bottom),
          borderTopWidth: 1,
          borderTopColor: colors.line,
        }}
      >
        <Button
          title={
            busy
              ? "正在准备…"
              : uploading
                ? "上传到这里 · 选择手机文件"
                : "下载所选文件"
          }
          disabled={
            busy ||
            loading ||
            Boolean(error) ||
            !space ||
            (uploading
              ? space.writable === false || Boolean(space.viewing)
              : !chosen)
          }
          onPress={() => void submit()}
        />
      </View>
    </View>
  );
}
