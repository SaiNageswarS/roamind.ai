"""Envelope models mirroring /proto/envelope.proto.

Wire format matches `protojson` (lowerCamelCase keys, RFC3339 timestamps)
so payloads round-trip with the Go gateway over Redis Streams.
"""

from __future__ import annotations

from datetime import datetime, timezone
from typing import List, Optional

from pydantic import BaseModel, ConfigDict, Field


class Attachment(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    name: str = ""
    mime_type: str = Field(default="", alias="mimeType")
    # protojson serializes `bytes` as base64-encoded string.
    data: str = ""


class TaskIn(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    id: str
    trace_id: str = Field(default="", alias="traceId")
    user_id: str = Field(default="", alias="userId")
    conversation_id: str = Field(default="", alias="conversationId")
    channel: str = ""
    channel_msg_id: str = Field(default="", alias="channelMsgId")
    text: str = ""
    attachments: List[Attachment] = Field(default_factory=list)
    received_at: Optional[datetime] = Field(default=None, alias="receivedAt")


class TaskOut(BaseModel):
    model_config = ConfigDict(populate_by_name=True)

    id: str
    trace_id: str = Field(default="", alias="traceId")
    in_reply_to: str = Field(default="", alias="inReplyTo")
    user_id: str = Field(default="", alias="userId")
    channel: str = ""
    chat_ref: str = Field(default="", alias="chatRef")
    intent: str = "reply"
    payload: str = ""
    created_at: datetime = Field(
        default_factory=lambda: datetime.now(timezone.utc),
        alias="createdAt",
    )

    def to_wire_json(self) -> str:
        """Serialize using protojson-compatible camelCase keys."""
        return self.model_dump_json(by_alias=True, exclude_none=False)


def parse_task_in(raw: str | bytes) -> TaskIn:
    return TaskIn.model_validate_json(raw)
