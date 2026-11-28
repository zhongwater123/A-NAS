// Sentences more than one page says, and the shell's own.
export const shared = {
  title: "剪辑中心",
  back: "返回",
  download: "下载",
  workGone: "作品已不存在",
  loadFailed: "没能读取作品",
  windowTooSmall: "请放大窗口",
  busy: "上一步还在处理",
  readOnly: "导出中，暂不能修改",
  sampleReadOnly: "示例作品不能修改",
  retry: "重试",
  tooManyRetries: "多次失败，请联系我们",
  uploadInterrupted: "上传中断，重新选择文件继续",
  queued: "排队中 · 上一条完成后自动开始",
  failure: {
    network: "网络中断",
    corrupt_file: "文件损坏",
    busy: "服务繁忙",
    too_few_assets: "素材不足",
  },
} as const;
