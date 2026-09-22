package whatsapp

import "testing"

// Vector shared with the API side (tests/test_webhook_auth.py::test_cross_language_vector).
func TestSignReceiptVector(t *testing.T) {
	got := signReceipt("s3cr3t", "1758549600", []byte(`{"a":1}`))
	want := "sha256=0752fd3d7900d88468f4e60389cc6b96c6518ac58012d32432dbead6ed917be7"
	if got != want {
		t.Fatalf("signReceipt = %s, want %s", got, want)
	}
}
