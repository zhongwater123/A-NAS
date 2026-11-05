import React, { useState, useSyncExternalStore } from "react";
import { FlatList, Text, View } from "react-native";
import { router } from "expo-router";
import { Upload, Download } from "lucide-react-native";
import { useRuntime } from "../../runtime";
import type { TransferEngine, Job } from "../../transfers/engine";
import { errorText } from "../../nas/client";
import {
  Button,
  Loading,
  Notice,
  bytes,
  colors,
  styles,
} from "../../ui/components";
const labels: Record<Job["state"], string> = {
  queued: "等待传输",
  running: "正在传输",
  paused: "已暂停",
  completed: "已完成",
  failed: "传输失败",
  canceled: "已取消",
};
export default function Transfers() {
  const { transfers } = useRuntime();
  return transfers ? <Jobs engine={transfers} /> : <Loading />;
}
function Jobs({ engine }: { engine: TransferEngine }) {
  const jobs = useSyncExternalStore(engine.subscribe, engine.snapshot);
  const [error, setError] = useState("");
  const act = (action: () => Promise<unknown>) => {
    setError("");
    void action().catch((e) => setError(errorText(e)));
  };
  return (
    <View style={styles.page}>
      <FlatList
        data={jobs}
        keyExtractor={(job) => job.id}
        contentContainerStyle={styles.body}
        ListHeaderComponent={
          <View style={{ gap: 14, marginBottom: 22 }}>
            <Text style={styles.title}>文件，随身往来</Text>
            <Text style={styles.muted}>
              从手机存到 NAS，也从 NAS 带到身边。
            </Text>
            <View style={styles.row}>
              <View style={{ flex: 1 }}>
                <Button
                  title="上传到 NAS"
                  onPress={() =>
                    router.push({
                      pathname: "/files",
                      params: { mode: "upload" },
                    })
                  }
                />
              </View>
              <View style={{ flex: 1 }}>
                <Button
                  title="从 NAS 下载"
                  quiet
                  onPress={() =>
                    router.push({
                      pathname: "/files",
                      params: { mode: "download" },
                    })
                  }
                />
              </View>
            </View>
            <Notice text={error} />
            <Text style={[styles.label, { marginTop: 12 }]}>本机传输</Text>
          </View>
        }
        ListEmptyComponent={
          <View style={styles.box}>
            <Text style={styles.text}>还没有传输记录</Text>
            <Text style={styles.muted}>
              选择手机文件上传，或从 NAS 下载一份资料。传输进度会显示在这里。
            </Text>
          </View>
        }
        ItemSeparatorComponent={() => <View style={{ height: 20 }} />}
        renderItem={({ item: job }) => (
          <View
            style={{
              gap: 12,
              paddingBottom: 20,
              borderBottomWidth: 1,
              borderBottomColor: colors.line,
            }}
          >
            <View style={styles.row}>
              {job.direction === "upload" ? (
                <Upload size={22} color={colors.muted} />
              ) : (
                <Download size={22} color={colors.muted} />
              )}
              <View style={{ flex: 1, gap: 4 }}>
                <Text numberOfLines={2} style={styles.text}>
                  {job.name}
                </Text>
                <Text style={styles.muted}>
                  {job.direction === "upload" ? "手机 → NAS" : "NAS → 手机"} ·{" "}
                  {labels[job.state]}
                </Text>
              </View>
            </View>
            <View
              accessibilityRole="progressbar"
              accessibilityValue={{
                min: 0,
                max: 100,
                now: job.size
                  ? Math.floor((job.done / job.size) * 100)
                  : job.state === "completed"
                    ? 100
                    : 0,
              }}
              style={{
                height: 3,
                backgroundColor: colors.line,
                borderRadius: 2,
              }}
            >
              <View
                style={{
                  height: 3,
                  backgroundColor: colors.ink,
                  borderRadius: 2,
                  width: `${job.size ? Math.min(100, (job.done / job.size) * 100) : job.state === "completed" ? 100 : 0}%`,
                }}
              />
            </View>
            <Text style={styles.muted}>
              {bytes(job.done)} / {bytes(job.size)}
            </Text>
            <Notice text={job.error} />
            <View style={styles.row}>
              {["queued", "running"].includes(job.state) && (
                <Button
                  title="暂停"
                  quiet
                  onPress={() => act(() => engine.pause(job.id))}
                />
              )}
              {["paused", "failed"].includes(job.state) && (
                <Button
                  title={job.state === "failed" ? "重试" : "继续"}
                  quiet
                  onPress={() => engine.resume(job.id)}
                />
              )}
              {!["completed", "canceled"].includes(job.state) && (
                <Button
                  title="取消"
                  quiet
                  onPress={() => act(() => engine.cancel(job.id))}
                />
              )}
              {job.state === "completed" && job.direction === "download" && (
                <Button
                  title="打开 / 保存到手机"
                  quiet
                  onPress={() => act(() => engine.share(job))}
                />
              )}
            </View>
          </View>
        )}
      />
    </View>
  );
}
