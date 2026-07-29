import threading
from typing import Any

import pytest
import zmq
from rustic_ai.core.messaging.core.message import AgentTag, Message
from rustic_ai.core.utils.gemstone_id import EPOCH, GemstoneID
from rustic_ai.core.utils.priority import Priority
from rustic_ai.forge.messaging.supervisor_backend import SupervisorZmqMessagingBackend


def make_message(id_obj: GemstoneID, topic: str, value: str) -> Message:
    return Message(
        id_obj=id_obj,
        topics=topic,
        sender=AgentTag(id="sender-1", name="sender"),
        payload={"value": value},
        topic_published_to=topic.split(":", 1)[-1],
    )


class FakeSupervisorBridge:
    def __init__(self) -> None:
        self.context = zmq.Context()
        self.socket = self.context.socket(zmq.PAIR)
        port = self.socket.bind_to_random_port("tcp://127.0.0.1")
        self.endpoint = f"tcp://127.0.0.1:{port}"
        self.shutdown_event = threading.Event()
        self.outbound: list[dict[str, Any]] = []
        self.outbound_lock = threading.Lock()
        self.requests: list[dict[str, Any]] = []
        self.requests_lock = threading.Lock()
        self.messages_by_topic: dict[str, list[Message]] = {}
        self.messages_by_id: dict[int, Message] = {}
        self.subscriptions: set[str] = set()
        self.thread = threading.Thread(target=self._serve, daemon=True)
        self.thread.start()

    def push_delivery(self, topic: str, message: Message) -> None:
        with self.outbound_lock:
            self.outbound.append(
                {
                    "kind": "event",
                    "op": "deliver",
                    "topic": topic,
                    "message": message.to_json(),
                }
            )

    def recorded_ops(self) -> list[str]:
        with self.requests_lock:
            return [request["op"] for request in self.requests]

    def requests_for(self, op: str) -> list[dict[str, Any]]:
        with self.requests_lock:
            return [request for request in self.requests if request.get("op") == op]

    def close(self) -> None:
        self.shutdown_event.set()
        self.thread.join(timeout=2)
        self.socket.close(linger=0)
        self.context.term()

    def _serve(self) -> None:
        poller = zmq.Poller()
        poller.register(self.socket, zmq.POLLIN)

        while not self.shutdown_event.is_set():
            events = dict(poller.poll(50))
            if self.socket in events and events[self.socket] & zmq.POLLIN:
                request = self.socket.recv_json()
                with self.requests_lock:
                    self.requests.append(request)
                response = self._handle_request(request)
                if response is not None:
                    self.socket.send_json(response)

            self._drain_outbound()

    def _drain_outbound(self) -> None:
        with self.outbound_lock:
            outbound = list(self.outbound)
            self.outbound.clear()

        for envelope in outbound:
            self.socket.send_json(envelope)

    def _handle_request(self, request: dict[str, Any]) -> dict[str, Any]:
        op = request["op"]
        request_id = request["request_id"]

        if op == "ping":
            return {"kind": "response", "op": op, "request_id": request_id, "ok": True}

        if op == "publish":
            message = Message.from_json(request["message"])
            topic = request["topic"]
            self.messages_by_topic.setdefault(topic, []).append(message)
            self.messages_by_id[message.id] = message
            return {"kind": "response", "op": op, "request_id": request_id, "ok": True}

        if op == "subscribe":
            self.subscriptions.add(request["topic"])
            return {"kind": "response", "op": op, "request_id": request_id, "ok": True}

        if op == "unsubscribe":
            self.subscriptions.discard(request["topic"])
            return {"kind": "response", "op": op, "request_id": request_id, "ok": True}

        if op == "get_messages":
            messages = [
                message.to_json()
                for message in self.messages_by_topic.get(request["topic"], [])
            ]
            return {
                "kind": "response",
                "op": op,
                "request_id": request_id,
                "ok": True,
                "messages": messages,
            }

        if op == "get_since":
            cursor_timestamp = GemstoneID.from_int(request["since_id"]).timestamp
            messages = [
                message.to_json()
                for message in sorted(
                    self.messages_by_topic.get(request["topic"], []),
                    key=lambda candidate: candidate.id,
                )
                if message.timestamp > cursor_timestamp
            ]
            return {
                "kind": "response",
                "op": op,
                "request_id": request_id,
                "ok": True,
                "messages": messages,
            }

        if op == "get_next":
            cursor_timestamp = GemstoneID.from_int(request["since_id"]).timestamp
            candidates = sorted(
                [
                    message
                    for message in self.messages_by_topic.get(request["topic"], [])
                    if message.timestamp > cursor_timestamp
                ],
                key=lambda candidate: candidate.id,
            )
            next_message = candidates[0].to_json() if candidates else None
            return {
                "kind": "response",
                "op": op,
                "request_id": request_id,
                "ok": True,
                "message": next_message,
            }

        if op == "get_by_id":
            msg_ids = request.get("msg_ids", request.get("message_ids", []))
            messages = [
                self.messages_by_id[msg_id].to_json()
                for msg_id in msg_ids
                if msg_id in self.messages_by_id
            ]
            return {
                "kind": "response",
                "op": op,
                "request_id": request_id,
                "ok": True,
                "messages": messages,
            }

        if op == "cleanup":
            self.subscriptions.clear()
            return {"kind": "response", "op": op, "request_id": request_id, "ok": True}

        return {
            "kind": "response",
            "op": op,
            "request_id": request_id,
            "ok": False,
            "error": f"unsupported op {op}",
        }


@pytest.fixture
def bridge() -> FakeSupervisorBridge:
    fake = FakeSupervisorBridge()
    try:
        yield fake
    finally:
        fake.close()


def test_supervisor_zmq_backend_round_trip_and_delivery(
    bridge: FakeSupervisorBridge,
) -> None:
    backend = SupervisorZmqMessagingBackend(
        endpoint=bridge.endpoint,
        request_timeout_ms=1_000,
        heartbeat_interval_ms=0,
        retry_enabled=False,
        crash_on_failure=False,
    )
    delivered: list[Message] = []
    delivered_event = threading.Event()

    try:
        base_timestamp = EPOCH + 2_000_000
        message_1 = make_message(
            GemstoneID(Priority.NORMAL, base_timestamp, 7, 0),
            "guild-1:alpha",
            "first",
        )
        message_2 = make_message(
            GemstoneID(Priority.NORMAL, base_timestamp + 1, 7, 0),
            "guild-1:alpha",
            "second",
        )

        backend.store_message("guild-1", "guild-1:alpha", message_1)
        backend.store_message("guild-1", "guild-1:alpha", message_2)

        history = backend.get_messages_for_topic("guild-1:alpha")
        assert [message.id for message in history] == [message_1.id, message_2.id]

        since = backend.get_messages_for_topic_since("guild-1:alpha", message_1.id)
        assert [message.id for message in since] == [message_2.id]

        next_message = backend.get_next_message_for_topic_since(
            "guild-1:alpha", message_1.id
        )
        assert next_message is not None
        assert next_message.id == message_2.id

        by_id = backend.get_messages_by_id("guild-1", [message_2.id, message_1.id])
        assert [message.id for message in by_id] == [message_2.id, message_1.id]
        assert bridge.requests_for("get_by_id")[-1]["msg_ids"] == [
            message_2.id,
            message_1.id,
        ]

        assert backend.load_subscribers("guild-1") == {}
        assert backend.supports_subscription() is True

        backend.subscribe(
            "guild-1:alpha",
            lambda message: (delivered.append(message), delivered_event.set()),
        )
        bridge.push_delivery("guild-1:alpha", message_2)

        assert delivered_event.wait(timeout=1)
        assert [message.id for message in delivered] == [message_2.id]

        backend.unsubscribe("guild-1:alpha")
    finally:
        backend.cleanup()

    assert bridge.recorded_ops().count("cleanup") == 1
    assert "subscribe" in bridge.recorded_ops()
    assert "unsubscribe" in bridge.recorded_ops()


def test_supervisor_zmq_default_topic_priority_boundary(
    bridge: FakeSupervisorBridge,
) -> None:
    backend = SupervisorZmqMessagingBackend(
        endpoint=bridge.endpoint,
        request_timeout_ms=1_000,
        heartbeat_interval_ms=0,
        retry_enabled=False,
        crash_on_failure=False,
    )
    base_timestamp = EPOCH + 1_000_000
    boundary = GemstoneID(Priority.NORMAL, base_timestamp, 1, 0)
    same_millisecond = GemstoneID(Priority.NORMAL, base_timestamp, 1, 1)
    later_normal = GemstoneID(Priority.NORMAL, base_timestamp + 1, 1, 0)
    later_high = GemstoneID(Priority.HIGH, base_timestamp + 2, 1, 0)
    later_urgent = GemstoneID(Priority.URGENT, base_timestamp + 3, 1, 0)

    assert later_high.to_int() < boundary.to_int()
    assert later_urgent.to_int() < boundary.to_int()

    def message(label: str, id_obj: GemstoneID) -> Message:
        return Message(
            id_obj=id_obj,
            topics="default_topic",
            sender=AgentTag(id="sender-1", name="sender"),
            payload={"label": label},
            topic_published_to="default_topic",
        )

    fixtures = [
        message("boundary", boundary),
        message("same_millisecond", same_millisecond),
        message("later_normal", later_normal),
        message("later_high", later_high),
        message("later_urgent", later_urgent),
    ]

    try:
        for fixture in fixtures:
            backend.store_message(
                "guild-test",
                "guild-test:default_topic",
                fixture,
            )

        since = backend.get_messages_for_topic_since(
            "guild-test:default_topic",
            boundary.to_int(),
        )
        actual_ids = [candidate.id for candidate in since]
        expected_ids = [
            later_urgent.to_int(),
            later_high.to_int(),
            later_normal.to_int(),
        ]
        returned_details = [
            (
                f"{candidate.payload['label']}"
                f"(priority={int(candidate.priority)},"
                f"timestamp={int(candidate.timestamp)},id={candidate.id})"
            )
            for candidate in since
        ]
        assert actual_ids == expected_ids, (
            f"cursor={boundary.to_int()}"
            f"(priority={boundary.priority},timestamp={boundary.timestamp}), "
            f"returned={returned_details}"
        )
        assert all(
            candidate.topic_published_to == "default_topic" for candidate in since
        )

        next_message = backend.get_next_message_for_topic_since(
            "guild-test:default_topic",
            boundary.to_int(),
        )
        assert next_message is not None
        assert next_message.id == later_urgent.to_int()
    finally:
        backend.cleanup()
