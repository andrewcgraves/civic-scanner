package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestHashContentIsSHA256Hex(t *testing.T) {
	for content, want := range map[string]string{
		"":    "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"abc": "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
	} {
		got := HashContent(content)
		if got.String() != want || got.IsZero() {
			t.Errorf("HashContent(%q) = %q (zero %v), want %q", content, got, got.IsZero(), want)
		}
	}
}

func TestContentHashesCompareByValue(t *testing.T) {
	if HashContent("agenda") != HashContent("agenda") {
		t.Error("equal content gave different hashes")
	}
	if HashContent("agenda") == HashContent("agenda v2") {
		t.Error("different content gave equal hashes")
	}
	if !(ContentHash{}).IsZero() {
		t.Error("zero ContentHash is not zero")
	}
}

func TestAddAgendaVersionRejectsZeroHashBeforeTouchingTheDatabase(t *testing.T) {
	in := AgendaVersionInput{MeetingID: uuid.New(), SourceURL: "https://example.com/a.pdf", Text: "agenda"}
	if _, _, err := New(nil).AddAgendaVersion(t.Context(), in); !errors.Is(err, ErrZeroContentHash) {
		t.Errorf("AddAgendaVersion err = %v, want ErrZeroContentHash", err)
	}
	if _, _, err := New(nil).AddAgendaVersionTx(t.Context(), nil, in); !errors.Is(err, ErrZeroContentHash) {
		t.Errorf("AddAgendaVersionTx err = %v, want ErrZeroContentHash", err)
	}
}
