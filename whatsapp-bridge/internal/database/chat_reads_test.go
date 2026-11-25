package database

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// MarkChatRead keeps only the newest read time of a chat, in UTC whole seconds.
func TestMarkChatReadKeepsNewest(t *testing.T) {
	db, err := sql.Open("sqlite3", "file:"+t.TempDir()+"/m.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := createTables(db); err != nil {
		t.Fatal(err)
	}
	store := &MessageStore{db: db}
	jerusalem := time.FixedZone("IST", 2*3600)
	later := time.Date(2026, 11, 25, 12, 0, 0, 500, jerusalem)
	earlier := later.Add(-time.Hour)
	for _, at := range []time.Time{later, earlier} {
		if err := store.MarkChatRead("111@lid", at); err != nil {
			t.Fatal(err)
		}
	}
	var got time.Time
	if err := db.QueryRow(`SELECT read_at FROM chat_reads WHERE chat_jid = ?`, "111@lid").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if !got.Equal(later.Truncate(time.Second)) {
		t.Fatalf("read_at = %v, want %v", got, later.Truncate(time.Second))
	}
}
