import React from "react";
import { Tabs, router } from "expo-router";
import { Pressable } from "react-native";
import { MessageCircle, ArrowDownUp, History } from "lucide-react-native";
import { Avatar, colors } from "../../ui/components";
export default function TabLayout() {
  return (
    <Tabs
      screenOptions={{
        headerStyle: { backgroundColor: colors.paper },
        headerShadowVisible: false,
        headerTitleStyle: { color: colors.ink, fontSize: 18 },
        tabBarActiveTintColor: colors.ink,
        tabBarInactiveTintColor: colors.muted,
        tabBarStyle: {
          backgroundColor: colors.paper,
          borderTopColor: colors.line,
        },
        tabBarLabelStyle: { fontSize: 12 },
        tabBarHideOnKeyboard: true,
        headerRight: () => (
          <Pressable
            accessibilityRole="button"
            accessibilityLabel="连接与账号"
            onPress={() => router.push("/settings")}
            style={{ padding: 12 }}
          >
            <Avatar size={32} />
          </Pressable>
        ),
      }}
    >
      <Tabs.Screen
        name="index"
        options={{
          title: "VNAS",
          tabBarLabel: "聊天",
          tabBarIcon: ({ color, size }) => (
            <MessageCircle color={color} size={size} />
          ),
          headerLeft: () => (
            <Pressable
              accessibilityRole="button"
              accessibilityLabel="历史对话"
              onPress={() => router.push("/history")}
              style={{ padding: 14 }}
            >
              <History color={colors.ink} size={23} />
            </Pressable>
          ),
        }}
      />
      <Tabs.Screen
        name="transfers"
        options={{
          title: "传输",
          tabBarIcon: ({ color, size }) => (
            <ArrowDownUp color={color} size={size} />
          ),
        }}
      />
    </Tabs>
  );
}
