import React, { useState } from "react";
import { Text, View } from "react-native";
import { type AssistantApproval, errorText } from "../nas/client";
import type { ChatController } from "./controller";
import { Button, Field, Notice, styles } from "../ui/components";
export function ApprovalCard({
  approval,
  chat,
}: {
  approval: AssistantApproval;
  chat: ChatController;
}) {
  const [busy, setBusy] = useState(false),
    [error, setError] = useState(""),
    [answers, setAnswers] = useState<Record<string, string>>({});
  async function decide(action: "confirm" | "reject" | "answer") {
    setBusy(true);
    setError("");
    try {
      await chat.decide(approval, action, answers);
    } catch (e) {
      setError(errorText(e));
    } finally {
      setBusy(false);
    }
  }
  return (
    <View style={styles.box}>
      <Text style={styles.label}>
        {approval.kind === "question" ? "VNAS 想确认一下" : "需要你确认"}
      </Text>
      {approval.change && (
        <>
          <Text style={styles.text}>{approval.change.summary}</Text>
          {approval.change.operations.map((operation, i) => (
            <Text key={i} selectable style={styles.muted}>
              {JSON.stringify(operation, null, 2)}
            </Text>
          ))}
        </>
      )}
      {approval.command && (
        <>
          <Text style={styles.text}>
            {approval.command.description || "执行以下命令"}
          </Text>
          <Text selectable style={styles.muted}>
            {approval.command.text}
          </Text>
        </>
      )}
      {approval.questions?.map((question) => (
        <View key={question.id} style={{ gap: 8 }}>
          <Field
            label={question.text}
            multiline
            value={answers[question.id] ?? ""}
            onChangeText={(value) =>
              setAnswers((old) => ({ ...old, [question.id]: value }))
            }
          />
          {question.choices?.map((choice) => (
            <Button
              key={choice}
              quiet
              title={choice}
              onPress={() =>
                setAnswers((old) => ({
                  ...old,
                  [question.id]: question.multiSelect
                    ? [old[question.id], choice].filter(Boolean).join("、")
                    : choice,
                }))
              }
            />
          ))}
        </View>
      ))}
      <Notice text={error} />
      <Button
        title={approval.kind === "question" ? "提交回答" : "允许这次操作"}
        disabled={busy}
        onPress={() =>
          void decide(approval.kind === "question" ? "answer" : "confirm")
        }
      />
      <Button
        title={approval.kind === "question" ? "跳过" : "拒绝"}
        quiet
        disabled={busy}
        onPress={() => void decide("reject")}
      />
    </View>
  );
}
