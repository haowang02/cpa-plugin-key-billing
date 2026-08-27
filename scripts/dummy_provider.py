#!/usr/bin/env python3

"""A dummy upstream provider for CLIProxyAPI.

It answers the four protocols CLIProxyAPI drives upstream — OpenAI chat
completions, OpenAI Responses, Anthropic Messages and Gemini generateContent —
streaming and not, for any model name, and reports usage in the shape each of
them actually uses. Every request reports the same token counts, which keeps the
end-to-end suite deterministic.

Point CLIProxyAPI at one instance with four provider entries:

    openai-compatibility:  base-url: http://127.0.0.1:PORT/v1
    codex-api-key:         base-url: http://127.0.0.1:PORT/v1
    claude-api-key:        base-url: http://127.0.0.1:PORT
    gemini-api-key:        base-url: http://127.0.0.1:PORT

Any API key is accepted.
"""

import argparse
import json
import random
import re
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import unquote, urlparse


# Input is large enough for one request to exhaust the suite's tiny quota plan.
# OpenAI and Anthropic expose both cache buckets; Gemini exposes cache reads and
# treats the would-be cache writes as ordinary input.
INPUT_TOKENS = 128
CACHE_READ_TOKENS = 32
CACHE_WRITE_TOKENS = 16
TOKENS = ("OK", " from", " the", " dummy", " provider", " for", " CLIProxyAPI", ".")
TTFT_SECONDS = 0.2
TOKEN_INTERVAL_SECONDS = 1 / 200
JITTER_RATIO = 0.1

PATHS = {
    "/v1/chat/completions": "chat",
    "/v1/responses": "responses",
    "/v1/messages": "anthropic",
}
GEMINI_PATH = re.compile(r"^/v1beta/models/(?P<model>.+):(?P<action>generateContent|streamGenerateContent)$")


class Turn:
    """One fixed answer represented in each provider's native schema."""

    def __init__(self, model):
        self.model = model
        self.id = uuid.uuid4().hex
        self.created = int(time.time())
        self.input_total = INPUT_TOKENS
        self.tokens = TOKENS
        self.output_tokens = len(TOKENS)
        self.text = "".join(self.tokens)

    @property
    def total_tokens(self):
        return self.input_total + self.output_tokens

    def chat_usage(self):
        return {
            "prompt_tokens": self.input_total,
            "completion_tokens": self.output_tokens,
            "total_tokens": self.total_tokens,
            "prompt_tokens_details": {
                "cached_tokens": CACHE_READ_TOKENS,
                "cache_creation_tokens": CACHE_WRITE_TOKENS,
            },
        }

    def responses_usage(self):
        return {
            "input_tokens": self.input_total,
            "output_tokens": self.output_tokens,
            "total_tokens": self.total_tokens,
            "input_tokens_details": {
                "cached_tokens": CACHE_READ_TOKENS,
                "cache_creation_tokens": CACHE_WRITE_TOKENS,
            },
        }

    def anthropic_usage(self, output_tokens=None):
        return {
            "input_tokens": self.input_total - CACHE_READ_TOKENS - CACHE_WRITE_TOKENS,
            "cache_read_input_tokens": CACHE_READ_TOKENS,
            "cache_creation_input_tokens": CACHE_WRITE_TOKENS,
            "output_tokens": self.output_tokens if output_tokens is None else output_tokens,
        }

    def gemini_usage(self):
        return {
            "promptTokenCount": self.input_total,
            "cachedContentTokenCount": CACHE_READ_TOKENS,
            "candidatesTokenCount": self.output_tokens,
            "totalTokenCount": self.total_tokens,
        }


class DummyProviderHandler(BaseHTTPRequestHandler):
    # HTTP/1.1 keeps repeated E2E requests on one connection, so every response
    # declares its length or uses chunked encoding.
    protocol_version = "HTTP/1.1"
    server_version = "dummy-provider"

    def do_GET(self):
        if urlparse(self.path).path in ("/health", "/healthz"):
            self.send_json(200, {"status": "ok"})
        else:
            self.send_error_body(404, f"no route for {self.path}")

    def do_POST(self):
        path = urlparse(self.path).path
        gemini = GEMINI_PATH.match(path)
        protocol = "gemini" if gemini else PATHS.get(path)
        if protocol is None:
            self.send_error_body(404, f"no route for {path}")
            return
        body = self.read_json_body()
        if body is None:
            self.send_error_body(400, "request body is not JSON")
            return

        if gemini:
            model = unquote(gemini["model"])
            stream = gemini["action"] == "streamGenerateContent"
        else:
            model = str(body.get("model") or "dummy")
            stream = bool(body.get("stream"))

        turn = Turn(model)
        if not stream:
            self.pause(TTFT_SECONDS)
            for _ in turn.tokens[1:]:
                self.pause(TOKEN_INTERVAL_SECONDS)
            self.send_json(200, self.non_stream_payload(protocol, turn))
            return
        stream_options = body.get("stream_options") or {}
        self.send_stream(protocol, turn, bool(stream_options.get("include_usage")))

    def non_stream_payload(self, protocol, turn):
        if protocol == "chat":
            return {
                "id": f"chatcmpl-{turn.id}",
                "object": "chat.completion",
                "created": turn.created,
                "model": turn.model,
                "choices": [
                    {
                        "index": 0,
                        "message": {"role": "assistant", "content": turn.text},
                        "finish_reason": "stop",
                    }
                ],
                "usage": turn.chat_usage(),
            }
        if protocol == "responses":
            return self.responses_envelope(turn, "completed", [self.responses_item(turn)], turn.responses_usage())
        if protocol == "anthropic":
            return {
                "id": f"msg_{turn.id}",
                "type": "message",
                "role": "assistant",
                "model": turn.model,
                "content": [{"type": "text", "text": turn.text}],
                "stop_reason": "end_turn",
                "stop_sequence": None,
                "usage": turn.anthropic_usage(),
            }
        return {
            "candidates": [
                {
                    "content": {"role": "model", "parts": [{"text": turn.text}]},
                    "finishReason": "STOP",
                    "index": 0,
                }
            ],
            "usageMetadata": turn.gemini_usage(),
            "modelVersion": turn.model,
            "responseId": turn.id,
        }

    def responses_item(self, turn, status="completed", text=None):
        return {
            "id": f"msg_{turn.id}",
            "type": "message",
            "status": status,
            "role": "assistant",
            "content": [{"type": "output_text", "text": turn.text if text is None else text, "annotations": []}],
        }

    def responses_envelope(self, turn, status, output, usage=None):
        return {
            "id": f"resp_{turn.id}",
            "object": "response",
            "created_at": turn.created,
            "status": status,
            "model": turn.model,
            "output": output,
            "usage": usage,
        }

    def send_stream(self, protocol, turn, include_usage):
        emitters = {
            "chat": lambda: self.stream_chat(turn, include_usage),
            "responses": lambda: self.stream_responses(turn),
            "anthropic": lambda: self.stream_anthropic(turn),
            "gemini": lambda: self.stream_gemini(turn),
        }
        self.pause(TTFT_SECONDS)
        self.start_stream()
        try:
            first_event = True
            for event, payload in emitters[protocol]():
                if not first_event:
                    self.pause(TOKEN_INTERVAL_SECONDS)
                self.write_event(event, payload)
                first_event = False
            self.write_chunk(b"")
        except (BrokenPipeError, ConnectionResetError):
            self.close_connection = True

    def stream_chat(self, turn, include_usage):
        # OpenAI names no events and ends with [DONE]; usage arrives in a
        # trailing choice-less chunk, and only when it was asked for.
        def chunk(choices, usage=None):
            payload = {
                "id": f"chatcmpl-{turn.id}",
                "object": "chat.completion.chunk",
                "created": turn.created,
                "model": turn.model,
                "choices": choices,
            }
            if include_usage:
                payload["usage"] = usage
            return payload

        yield None, chunk([{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": None}])
        for token in turn.tokens:
            yield None, chunk([{"index": 0, "delta": {"content": token}, "finish_reason": None}])
        yield None, chunk([{"index": 0, "delta": {}, "finish_reason": "stop"}])
        if include_usage:
            yield None, chunk([], usage=turn.chat_usage())
        yield None, "[DONE]"

    def stream_responses(self, turn):
        # The Responses API names every event and ends on response.completed,
        # which is where the usage of the whole turn rides.
        item_id = f"msg_{turn.id}"
        position = {"item_id": item_id, "output_index": 0, "content_index": 0}
        sequence = iter(range(10_000_000))

        def event(kind, **fields):
            return kind, {"type": kind, "sequence_number": next(sequence), **fields}

        in_progress = self.responses_envelope(turn, "in_progress", [])
        yield event("response.created", response=in_progress)
        yield event("response.in_progress", response=in_progress)
        yield event("response.output_item.added", output_index=0, item=self.responses_item(turn, "in_progress", ""))
        yield event("response.content_part.added", part={"type": "output_text", "text": "", "annotations": []}, **position)
        for token in turn.tokens:
            yield event("response.output_text.delta", delta=token, **position)
        yield event("response.output_text.done", text=turn.text, **position)
        yield event(
            "response.content_part.done",
            part={"type": "output_text", "text": turn.text, "annotations": []},
            **position,
        )
        yield event("response.output_item.done", output_index=0, item=self.responses_item(turn))
        yield event(
            "response.completed",
            response=self.responses_envelope(turn, "completed", [self.responses_item(turn)], turn.responses_usage()),
        )

    def stream_anthropic(self, turn):
        # message_start keeps its usage under message.usage, which upstream
        # readers skip; message_delta is the event reporting the settled counts,
        # so it repeats the input buckets alongside the output total.
        yield "message_start", {
            "type": "message_start",
            "message": {
                "id": f"msg_{turn.id}",
                "type": "message",
                "role": "assistant",
                "model": turn.model,
                "content": [],
                "stop_reason": None,
                "stop_sequence": None,
                "usage": turn.anthropic_usage(output_tokens=0),
            },
        }
        yield "content_block_start", {
            "type": "content_block_start",
            "index": 0,
            "content_block": {"type": "text", "text": ""},
        }
        for token in turn.tokens:
            yield "content_block_delta", {
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "text_delta", "text": token},
            }
        yield "content_block_stop", {"type": "content_block_stop", "index": 0}
        yield "message_delta", {
            "type": "message_delta",
            "delta": {"stop_reason": "end_turn", "stop_sequence": None},
            "usage": turn.anthropic_usage(),
        }
        yield "message_stop", {"type": "message_stop"}

    def stream_gemini(self, turn):
        # Gemini may repeat usageMetadata on every chunk, but a reader that keeps
        # the first one it sees would then bill a partial answer. Only the last
        # chunk carries it, which is also where the finish reason lands.
        for index, token in enumerate(turn.tokens):
            candidate = {"content": {"role": "model", "parts": [{"text": token}]}, "index": 0}
            chunk = {"candidates": [candidate], "modelVersion": turn.model, "responseId": turn.id}
            if index == len(turn.tokens) - 1:
                candidate["finishReason"] = "STOP"
                chunk["usageMetadata"] = turn.gemini_usage()
            yield None, chunk

    def read_json_body(self):
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        if not raw:
            return {}
        try:
            body = json.loads(raw)
        except (ValueError, UnicodeDecodeError):
            return None
        return body if isinstance(body, dict) else {}

    @staticmethod
    def pause(duration):
        time.sleep(duration * random.uniform(1 - JITTER_RATIO, 1 + JITTER_RATIO))

    # Nothing reaches this that a provider would answer, so one shape does for
    # all four protocols.
    def send_error_body(self, status, message):
        self.send_json(status, {"error": {"message": f"dummy provider: {message}", "code": status}})

    def send_json(self, status, payload):
        body = self.encode(payload)
        self.send_response(status)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        try:
            self.wfile.write(body)
        except (BrokenPipeError, ConnectionResetError):
            self.close_connection = True

    def start_stream(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream; charset=utf-8")
        self.send_header("Cache-Control", "no-cache")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()

    def write_event(self, event, payload):
        data = payload.encode() if isinstance(payload, str) else self.encode(payload)
        prefix = f"event: {event}\n".encode() if event else b""
        self.write_chunk(prefix + b"data: " + data + b"\n\n")

    def write_chunk(self, data):
        self.wfile.write(b"%x\r\n" % len(data) + data + b"\r\n")
        self.wfile.flush()

    def encode(self, payload):
        return json.dumps(payload, ensure_ascii=False, separators=(",", ":")).encode()

    def log_message(self, message, *args):
        pass


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=8770, help="0 picks a free port")
    options = parser.parse_args()

    server = ThreadingHTTPServer((options.host, options.port), DummyProviderHandler)
    server.daemon_threads = True
    print(f"dummy provider: http://{options.host}:{server.server_port}", flush=True)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()


if __name__ == "__main__":
    main()
