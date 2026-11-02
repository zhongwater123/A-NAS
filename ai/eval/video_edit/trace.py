"""Reads a plan's trace.json: its spans, the stage each belongs to and its external calls.

The file is an ExportTraceServiceRequest in OTLP's JSON encoding
(docs/specs/video-edit-evaluation.md#方案目录). Integers may come as strings,
as protobuf's JSON mapping writes int64, and ids in hex or base64: only their
equality matters here. Standard library only.
"""

import json

PREFIX = "anas.external."


def attribute(value):
    for key in ("stringValue", "boolValue", "doubleValue"):
        if key in value:
            return value[key]
    if "intValue" in value:
        return int(value["intValue"])
    return None


def parse(document):
    spans = []
    for resource in document.get("resourceSpans", []):
        for scope in resource.get("scopeSpans", resource.get("instrumentationLibrarySpans", [])):
            spans.extend({
                "name": span.get("name", ""),
                "id": span.get("spanId", ""),
                "parent": span.get("parentSpanId") or "",
                "start": int(span.get("startTimeUnixNano", 0)),
                "end": int(span.get("endTimeUnixNano", 0)),
                "attributes": {item["key"]: attribute(item.get("value", {})) for item in span.get("attributes", [])},
            } for span in scope.get("spans", []))
    return spans


def load(path):
    with open(path, encoding="utf-8") as file:
        return parse(json.load(file))


def seconds(span):
    return max(0, span["end"] - span["start"]) / 1e9


def index(spans):
    return {span["id"]: span for span in spans if span["id"]}


def stage_of(span, by_id, stages):
    """The nearest span named after a stage, the span itself included; None outside every stage."""
    seen = set()
    while span is not None and id(span) not in seen:
        if span["name"] in stages:
            return span["name"]
        seen.add(id(span))
        span = by_id.get(span["parent"])
    return None


def stage_seconds(spans, stages):
    """Seconds per stage. A stage span inside a span of the same stage counts once."""
    by_id = index(spans)
    totals = dict.fromkeys(stages, 0.0)
    for span in spans:
        if span["name"] in stages:
            parent = by_id.get(span["parent"])
            if parent is None or stage_of(parent, by_id, stages) != span["name"]:
                totals[span["name"]] += seconds(span)
    return totals


def undeclared(spans, stages):
    """Names of spans that sit in no stage."""
    by_id = index(spans)
    return sorted({span["name"] for span in spans if stage_of(span, by_id, stages) is None})


def external_calls(spans, stages):
    by_id = index(spans)
    calls = []
    for span in spans:
        attributes = span["attributes"]
        if attributes.get(PREFIX + "target") is None:
            continue
        calls.append({
            "stage": stage_of(span, by_id, stages),
            "target": attributes[PREFIX + "target"],
            "service": attributes.get(PREFIX + "service") or "",
            "upload_bytes": int(attributes.get(PREFIX + "upload_bytes") or 0),
            "input_tokens": int(attributes.get(PREFIX + "input_tokens") or 0),
            "output_tokens": int(attributes.get(PREFIX + "output_tokens") or 0),
            "cost": float(attributes.get(PREFIX + "cost") or 0),
            "currency": attributes.get(PREFIX + "currency") or "",
            "seconds": seconds(span),
        })
    return calls
