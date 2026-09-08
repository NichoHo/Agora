"""OpenTelemetry setup, mirroring internal/tracing (Go) and services/risk's
copy of this same file as closely as a different language allows: same
OTLP/HTTP endpoint, same W3C propagation, same reserved "_trace" payload
key for the one hop otelhttp can't reach. See docs/adr/0004-distributed-tracing.md.
"""

import logging
import os

from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.propagate import extract
from opentelemetry.sdk.resources import SERVICE_NAME, Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor

log = logging.getLogger("assist.tracing")


def init(service_name: str) -> None:
    endpoint = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "")
    if not endpoint:
        log.warning("OTEL_EXPORTER_OTLP_ENDPOINT unset; tracing disabled")
        return
    provider = TracerProvider(resource=Resource.create({SERVICE_NAME: service_name}))
    provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter(endpoint=f"http://{endpoint}/v1/traces")))
    trace.set_tracer_provider(provider)
    log.info("tracing enabled endpoint=%s", endpoint)


def context_from_payload(payload: dict):
    """Extracts the parent span context a Go service embedded under
    "_trace" (internal/tracing.Traceparent), so the span this consumer
    starts becomes a real child, not a disconnected trace."""
    tp = payload.get("_trace")
    if not tp:
        return None
    return extract({"traceparent": tp})
