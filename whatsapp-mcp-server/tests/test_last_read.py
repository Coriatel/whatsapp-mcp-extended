"""list_chats: last_read_time comes from the bridge's chat_reads, and an older store without it still works."""

import sqlite3

import pytest

import whatsapp


def make(path, with_reads):
    conn = sqlite3.connect(path)
    conn.execute("CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP)")
    conn.execute("CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, "
                 "is_from_me BOOLEAN, PRIMARY KEY (id, chat_jid))")
    conn.execute("INSERT INTO chats VALUES ('a@lid', 'A', '2026-11-25 10:00:00+00:00')")
    conn.execute("INSERT INTO chats VALUES ('b@lid', 'B', '2026-11-25 09:00:00+00:00')")
    if with_reads:
        conn.execute("CREATE TABLE chat_reads (chat_jid TEXT PRIMARY KEY, read_at TIMESTAMP)")
        conn.execute("INSERT INTO chat_reads VALUES ('a@lid', '2026-11-25 10:05:00+00:00')")
    conn.commit()
    conn.close()


@pytest.mark.parametrize("with_reads", [True, False])
def test_last_read_time(tmp_path, monkeypatch, with_reads):
    db = str(tmp_path / "messages.db")
    make(db, with_reads)
    monkeypatch.setattr(whatsapp, "MESSAGES_DB_PATH", db)
    rows = {r["jid"]: r for r in whatsapp.list_chats(limit=10)}
    assert rows["a@lid"]["last_read_time"] == ("2026-11-25T10:05:00+00:00" if with_reads else None)
    assert rows["b@lid"]["last_read_time"] is None
