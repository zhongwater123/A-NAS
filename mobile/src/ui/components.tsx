import React from "react";
import {
  ActivityIndicator,
  Image,
  Pressable,
  StyleSheet,
  Text,
  TextInput,
  View,
  type TextInputProps,
} from "react-native";

export const colors = {
  paper: "#FCFCFA",
  ink: "#202320",
  muted: "#656A65",
  line: "#E5E7E2",
  soft: "#F0F2ED",
  green: "#357757",
  danger: "#A33232",
};
export const styles = StyleSheet.create({
  page: { flex: 1, backgroundColor: colors.paper },
  body: { padding: 22, gap: 18 },
  title: { fontSize: 27, lineHeight: 35, fontWeight: "600", color: colors.ink },
  text: { fontSize: 16, lineHeight: 25, color: colors.ink },
  muted: { fontSize: 14, lineHeight: 22, color: colors.muted },
  label: { fontSize: 14, fontWeight: "600", color: colors.ink },
  row: { flexDirection: "row", alignItems: "center", gap: 12 },
  box: {
    borderWidth: 1,
    borderColor: colors.line,
    borderRadius: 16,
    padding: 18,
    gap: 12,
  },
  input: {
    borderWidth: 1,
    borderColor: colors.line,
    backgroundColor: "#FFF",
    borderRadius: 12,
    minHeight: 50,
    paddingHorizontal: 14,
    paddingVertical: 12,
    color: colors.ink,
    fontSize: 16,
  },
});
export function Button({
  title,
  onPress,
  disabled,
  quiet,
  danger,
}: {
  title: string;
  onPress(): void;
  disabled?: boolean;
  quiet?: boolean;
  danger?: boolean;
}) {
  return (
    <Pressable
      accessibilityRole="button"
      accessibilityLabel={title}
      disabled={disabled}
      onPress={onPress}
      style={({ pressed }) => ({
        minHeight: 46,
        borderRadius: 12,
        paddingHorizontal: 16,
        paddingVertical: 12,
        alignItems: "center",
        justifyContent: "center",
        backgroundColor: quiet ? colors.soft : colors.ink,
        opacity: disabled ? 0.4 : pressed ? 0.7 : 1,
      })}
    >
      <Text
        style={{
          fontSize: 15,
          fontWeight: "600",
          color: danger ? colors.danger : quiet ? colors.ink : "#FFF",
        }}
      >
        {title}
      </Text>
    </Pressable>
  );
}
export function Field({ label, ...props }: TextInputProps & { label: string }) {
  return (
    <View style={{ gap: 8 }}>
      <Text style={styles.label}>{label}</Text>
      <TextInput
        accessibilityLabel={label}
        placeholderTextColor={colors.muted}
        style={styles.input}
        {...props}
      />
    </View>
  );
}
export function Notice({ text }: { text?: string }) {
  return text ? (
    <Text
      accessibilityRole="alert"
      style={{ color: colors.danger, fontSize: 14, lineHeight: 22 }}
    >
      {text}
    </Text>
  ) : null;
}
export function Loading({ text = "正在载入…" }: { text?: string }) {
  return (
    <View style={[styles.row, { padding: 22 }]}>
      <ActivityIndicator color={colors.ink} />
      <Text style={styles.muted}>{text}</Text>
    </View>
  );
}
export function Avatar({ size = 38 }: { size?: number }) {
  return (
    <Image
      accessibilityLabel="VNAS"
      source={require("../../../web/src/assets/assistant-logo.webp")}
      style={{
        width: size,
        height: size,
        borderRadius: size / 2,
        borderWidth: 1,
        borderColor: colors.line,
      }}
    />
  );
}
export function bytes(value: number) {
  if (value < 1024) return `${value} B`;
  const units = ["KB", "MB", "GB", "TB"];
  let n = value / 1024,
    i = 0;
  while (n >= 1024 && i < 3) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(n < 10 ? 1 : 0)} ${units[i]}`;
}
