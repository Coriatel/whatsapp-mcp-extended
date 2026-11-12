"""When was the last one-to-one message with a phone number, and who wrote it.

Metadata only: no message text, no name and no chat id leaves this module.
WhatsApp files most one-to-one chats under a hidden id (LID), so a phone number
is looked up both as a phone chat and through whatsmeow_lid_map.

The bridge writes messages.db without a busy timeout, so a reader that stays
long makes it drop an inbound message. Both reads here are read-only, give up
after BUSY_S instead of waiting for a lock, are cut off after RUN_S of work
(messages has no index on the chat, so a miss would otherwise walk the whole
table), and the message lookup walks from the newest row backwards and stops
at the first hit.
"""
import re
import sqlite3
import time
from datetime import datetime
from typing import Any

BUSY_S = 0.2
RUN_S = 0.3
PHONE = re.compile(r"[1-9][0-9]{7,14}")


class StoreBusy(Exception):
    """The store could not be read right now; the caller answers "unavailable"."""


def _read_only(path: str) -> sqlite3.Connection:
    conn = sqlite3.connect(f"file:{path}?mode=ro", uri=True, timeout=BUSY_S)
    deadline = time.monotonic() + RUN_S
    conn.set_progress_handler(lambda: time.monotonic() > deadline, 1000)   # non-zero interrupts the query
    return conn


def contact_last_activity(phone: str, messages_db: str, whatsapp_db: str) -> dict[str, Any]:
    """{"match": False} or {"match": True, "last_at": ISO 8601, "last_from": "me" | "them" | None}.

    last_from is None when the store holds no message at the chat's last time (the last event was
    something the bridge does not keep, or the chat came from a history sync without its messages):
    the chat and its time are known, who wrote is not, and it is not guessed from an older message.
    """
    if not isinstance(phone, str) or not PHONE.fullmatch(phone):
        raise ValueError("phone: digits only, international, no plus sign")
    jids = [f"{phone}@s.whatsapp.net"]
    try:
        conn = _read_only(whatsapp_db)
        try:
            row = conn.execute("SELECT lid FROM whatsmeow_lid_map WHERE pn = ?", (phone,)).fetchone()
        finally:
            conn.close()
        if row and isinstance(row[0], str) and row[0].isdigit():
            jids.append(f"{row[0]}@lid")
        conn = _read_only(messages_db)
        try:
            marks = ",".join("?" * len(jids))
            chat = conn.execute(
                f"SELECT jid, last_message_time FROM chats WHERE jid IN ({marks}) "
                "AND last_message_time IS NOT NULL ORDER BY last_message_time DESC LIMIT 1", jids).fetchone()
            if not chat:
                return {"match": False}
            last = conn.execute(
                "SELECT is_from_me FROM messages WHERE chat_jid = ? AND timestamp = ? "
                "ORDER BY rowid DESC LIMIT 1", chat).fetchone()
        finally:
            conn.close()
    except sqlite3.Error as exc:
        raise StoreBusy(type(exc).__name__) from None
    try:
        at = datetime.fromisoformat(chat[1])
    except (TypeError, ValueError):
        return {"match": False}
    if at.tzinfo is None:
        return {"match": False}
    return {"match": True, "last_at": at.isoformat(), "last_from": None if not last else "me" if last[0] else "them"}
