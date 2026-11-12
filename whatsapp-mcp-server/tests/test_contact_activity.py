"""contact_activity: metadata only, one-to-one only, read-only, never waits on a busy store."""

import sqlite3

import pytest

import contact_activity as ca

PHONE, LID, OTHER = "972500000001", "111111111111111", "972500000002"


@pytest.fixture
def store(tmp_path):
    messages, whatsapp = str(tmp_path / "messages.db"), str(tmp_path / "whatsapp.db")
    conn = sqlite3.connect(whatsapp)
    conn.execute("CREATE TABLE whatsmeow_lid_map (lid TEXT PRIMARY KEY, pn TEXT UNIQUE NOT NULL)")
    conn.execute("INSERT INTO whatsmeow_lid_map VALUES (?, ?)", (LID, PHONE))
    conn.commit()
    conn.close()
    conn = sqlite3.connect(messages)
    conn.execute("CREATE TABLE chats (jid TEXT PRIMARY KEY, name TEXT, last_message_time TIMESTAMP)")
    conn.execute("CREATE TABLE messages (id TEXT, chat_jid TEXT, sender TEXT, content TEXT, timestamp TIMESTAMP, "
                 "is_from_me BOOLEAN, PRIMARY KEY (id, chat_jid))")
    conn.commit()
    conn.close()
    return messages, whatsapp


def add(messages_db, jid, when, from_me, text="SECRET WORDS", name="SECRET NAME"):
    conn = sqlite3.connect(messages_db)
    conn.execute("INSERT OR REPLACE INTO chats VALUES (?, ?, ?)", (jid, name, when))
    conn.execute("INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?)", (f"id-{jid}-{when}", jid, jid, text, when, from_me))
    conn.commit()
    conn.close()


def test_phone_chat(store):
    add(store[0], f"{OTHER}@s.whatsapp.net", "2026-11-12 10:00:00+02:00", 0)
    assert ca.contact_last_activity(OTHER, *store) == {
        "match": True, "last_at": "2026-11-12T10:00:00+02:00", "last_from": "them"}


def test_hidden_id_chat_is_found_by_phone(store):
    add(store[0], f"{LID}@lid", "2026-11-12 11:00:00+02:00", 1)
    assert ca.contact_last_activity(PHONE, *store) == {
        "match": True, "last_at": "2026-11-12T11:00:00+02:00", "last_from": "me"}


def test_the_newer_of_the_two_chats_wins(store):
    add(store[0], f"{PHONE}@s.whatsapp.net", "2026-01-01 08:00:00+02:00", 1)
    add(store[0], f"{LID}@lid", "2026-11-12 11:00:00+02:00", 0)
    assert ca.contact_last_activity(PHONE, *store)["last_from"] == "them"


def test_only_three_fields_and_no_words(store):
    add(store[0], f"{LID}@lid", "2026-11-12 11:00:00+02:00", 1)
    out = ca.contact_last_activity(PHONE, *store)
    assert sorted(out) == ["last_at", "last_from", "match"]
    assert "SECRET" not in repr(out) and LID not in repr(out)


def test_no_chat_and_groups_do_not_match(store):
    add(store[0], f"{OTHER}-1600000000@g.us", "2026-11-12 11:00:00+02:00", 0)
    add(store[0], "status@broadcast", "2026-11-12 11:00:00+02:00", 0)
    assert ca.contact_last_activity(OTHER, *store) == {"match": False}


@pytest.mark.parametrize("bad", ["", "+972500000001", "97250000000a", "0500000001", "1234567", "9" * 16,
                                 "972500000001' OR 1=1 --", "%", None, 972500000001])
def test_bad_phone_is_refused_before_any_read(store, bad):
    with pytest.raises(ValueError):
        ca.contact_last_activity(bad, "/nonexistent/messages.db", "/nonexistent/whatsapp.db")


def test_nothing_is_written(store):
    add(store[0], f"{LID}@lid", "2026-11-12 11:00:00+02:00", 1)
    seen = []
    real = ca._read_only

    def spy(path):
        conn = real(path)
        conn.set_trace_callback(seen.append)
        with pytest.raises(sqlite3.OperationalError):
            conn.execute("CREATE TABLE x (y)")
        return conn

    ca._read_only = spy
    try:
        ca.contact_last_activity(PHONE, *store)
    finally:
        ca._read_only = real
    assert seen and all(s.lstrip().upper().startswith(("SELECT", "CREATE TABLE X")) for s in seen)
    assert not any("content" in s.lower() or "name" in s.lower() for s in seen if s.lstrip().upper().startswith("SELECT"))


def test_a_locked_store_answers_busy_quickly(store):
    import time
    add(store[0], f"{LID}@lid", "2026-11-12 11:00:00+02:00", 1)
    writer = sqlite3.connect(store[0])
    writer.execute("BEGIN EXCLUSIVE")
    try:
        start = time.monotonic()
        with pytest.raises(ca.StoreBusy):
            ca.contact_last_activity(PHONE, *store)
        assert time.monotonic() - start < 2
    finally:
        writer.rollback()
        writer.close()


def test_a_missing_store_is_busy_not_a_crash(store):
    with pytest.raises(ca.StoreBusy):
        ca.contact_last_activity(PHONE, "/nonexistent/messages.db", "/nonexistent/whatsapp.db")


def test_a_long_walk_is_cut_off(store, monkeypatch):
    """A chat whose last message is far back (or missing) must not keep the read lock."""
    conn = sqlite3.connect(store[0])
    conn.execute("INSERT INTO chats VALUES (?, ?, ?)", (f"{LID}@lid", "n", "2026-11-12 11:00:00+02:00"))
    conn.executemany("INSERT INTO messages VALUES (?, ?, ?, ?, ?, ?)",
                     ((f"m{n}", "someone-else@lid", "s", "t", "2026-01-01 00:00:00+02:00", 0) for n in range(200000)))
    conn.commit()
    conn.close()
    monkeypatch.setattr(ca, "RUN_S", 0.0)
    import time
    start = time.monotonic()
    with pytest.raises(ca.StoreBusy):
        ca.contact_last_activity(PHONE, *store)
    assert time.monotonic() - start < 1
    monkeypatch.setattr(ca, "RUN_S", 5.0)
    assert ca.contact_last_activity(PHONE, *store) == {"match": False}   # no such message: no match, not a guess
