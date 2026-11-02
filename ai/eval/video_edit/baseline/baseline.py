"""The baseline plan: evenly spaced three-second cuts over all sources, as long as the case asks.

It is the evaluation's control (docs/specs/video-edit-evaluation.md): every
device runs it as its own time base. It also shows a plan how to write the
three outputs, trace.json with the OpenTelemetry SDK above all. It does not
read the instruction.

    /env/venv/bin/python baseline.py --case /case/case-01.json --out /out
"""

import argparse
import json
import math
import os
import subprocess
import tempfile

import opentimelineio as otio
from google.protobuf import json_format
from opentelemetry.exporter.otlp.proto.common.trace_encoder import encode_spans
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor, SpanExporter, SpanExportResult

FFMPEG = "/usr/bin/ffmpeg"
FFPROBE = "/usr/bin/ffprobe"
SEGMENT = 3.0
FPS = 30
LANDSCAPE, PORTRAIT = (1280, 720), (720, 1280)


class FileExporter(SpanExporter):
    """Keeps the finished spans and writes them as one OTLP JSON ExportTraceServiceRequest."""

    def __init__(self):
        self.spans = []

    def export(self, spans):
        self.spans.extend(spans)
        return SpanExportResult.SUCCESS

    def write(self, path):
        with open(path, "w", encoding="utf-8") as file:
            file.write(json_format.MessageToJson(encode_spans(self.spans)))


def probe(path):
    result = subprocess.run([FFPROBE, "-v", "error", "-show_entries",
                             "format=duration:stream=codec_type,width,height:stream_side_data=rotation", "-of", "json", path],
                            capture_output=True, text=True, check=True)
    data = json.loads(result.stdout)
    streams = data.get("streams", [])
    video = next(stream for stream in streams if stream.get("codec_type") == "video")
    rotation = next((int(item["rotation"]) for item in video.get("side_data_list", []) if "rotation" in item), 0)
    width, height = video["width"], video["height"]
    if abs(rotation) % 180 == 90:
        width, height = height, width
    return {
        "path": path,
        "seconds": float(data["format"]["duration"]),
        "portrait": height > width,
        "audio": any(stream.get("codec_type") == "audio" for stream in streams),
    }


def select(sources, target):
    """(source, start, seconds) cuts centred at even steps over the sources laid end to end."""
    total = sum(source["seconds"] for source in sources)
    count = max(1, math.ceil(target / SEGMENT))
    length = min(SEGMENT, total / count)
    cuts, offset = [], 0.0
    bounds = []
    for source in sources:
        bounds.append((offset, offset + source["seconds"], source))
        offset += source["seconds"]
    for number in range(count):
        centre = (number + 0.5) * total / count
        begin, end, source = next(bound for bound in bounds if bound[0] <= centre <= bound[1])
        start = min(max(centre - length / 2, begin), end - length) - begin
        cuts.append((source, max(start, 0.0), min(length, source["seconds"])))
    return cuts


def timeline(name, cuts):
    track = otio.schema.Track(name="video")
    for source, start, seconds in cuts:
        track.append(otio.schema.Clip(
            name=os.path.basename(source["path"]),
            media_reference=otio.schema.ExternalReference(target_url=source["path"]),
            source_range=otio.opentime.TimeRange(otio.opentime.RationalTime(round(start * FPS), FPS),
                                                 otio.opentime.RationalTime(round(seconds * FPS), FPS)),
        ))
    result = otio.schema.Timeline(name=name)
    result.tracks.append(track)
    return result


def render(cuts, size, path, work):
    width, height = size
    video = (f"scale={width}:{height}:force_original_aspect_ratio=decrease,pad={width}:{height}:(ow-iw)/2:(oh-ih)/2,"
             f"setsar=1,fps={FPS},format=yuv420p")
    parts = []
    for number, (source, start, seconds) in enumerate(cuts):
        part = os.path.join(work, f"part-{number:03d}.mp4")
        command = [FFMPEG, "-v", "error", "-y", "-ss", f"{start:.3f}", "-t", f"{seconds:.3f}", "-i", source["path"]]
        if not source["audio"]:
            command += ["-f", "lavfi", "-t", f"{seconds:.3f}", "-i", "anullsrc=r=48000:cl=stereo"]
        command += ["-map", "0:v:0", "-map", "0:a:0" if source["audio"] else "1:a:0", "-vf", video,
                    "-af", "aresample=48000", "-ac", "2", "-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
                    "-c:a", "aac", "-b:a", "128k", "-shortest", part]
        subprocess.run(command, check=True)
        parts.append(part)
    listing = os.path.join(work, "parts.txt")
    with open(listing, "w", encoding="utf-8") as file:
        file.writelines(f"file '{part}'\n" for part in parts)
    subprocess.run([FFMPEG, "-v", "error", "-y", "-f", "concat", "-safe", "0", "-i", listing, "-c", "copy",
                    "-movflags", "+faststart", path], check=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--case", required=True)
    parser.add_argument("--out", required=True)
    args = parser.parse_args()

    exporter = FileExporter()
    provider = TracerProvider()
    provider.add_span_processor(SimpleSpanProcessor(exporter))
    tracer = provider.get_tracer("anas.video_edit.baseline")
    with open(args.case, encoding="utf-8") as file:
        case = json.load(file)
    folder = os.path.dirname(os.path.abspath(args.case))
    try:
        with tracer.start_as_current_span("probe"):
            sources = [probe(os.path.join(folder, source["file"])) for source in case["sources"]]
        with tracer.start_as_current_span("select") as span:
            cuts = select(sources, case["request"]["target_seconds"])
            span.set_attribute("cuts", len(cuts))
            otio.adapters.write_to_file(timeline(case["id"], cuts), os.path.join(args.out, "timeline.otio"))
        with tracer.start_as_current_span("render"), tempfile.TemporaryDirectory() as work:
            size = PORTRAIT if sources[0]["portrait"] else LANDSCAPE
            render(cuts, size, os.path.join(args.out, "output.mp4"), work)
    finally:
        exporter.write(os.path.join(args.out, "trace.json"))


if __name__ == "__main__":
    main()
