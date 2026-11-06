import Foundation
import Vision

// Inspect only a fresh login screen: this CI image must contain no credentials.
do {
  let request = VNRecognizeTextRequest()
  request.recognitionLevel = .accurate
  request.recognitionLanguages = ["zh-Hans", "en-US"]
  try VNImageRequestHandler(url: URL(fileURLWithPath: CommandLine.arguments[1])).perform([request])
  let text = (request.results ?? []).compactMap { $0.topCandidates(1).first?.string }.joined(separator: "\n")
  print(text)
  let compact = text.filter { !$0.isWhitespace }
  let address = CommandLine.arguments.count > 2
    ? String(CommandLine.arguments[2].split(separator: ",")[0]) : "172.18.45.48"
  guard compact.contains("连接你的NAS"), compact.contains(address) else {
    fputs("Login screen or configured NAS address is missing\n", stderr)
    exit(2)
  }
  if compact.contains("连接未完成") || compact.contains("请求失败") {
    fputs("Fresh login screen unexpectedly shows an error\n", stderr)
    exit(1)
  }
} catch {
  fputs("Cannot inspect the login screenshot: \(error)\n", stderr)
  exit(2)
}
