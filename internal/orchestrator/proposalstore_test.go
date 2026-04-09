package orchestrator

import (
	"testing"
	"time"
)

func TestProposalStore_Stage(t *testing.T) {
	ps := NewProposalStore(10 * time.Minute)
	edits := []StoryEdit{
		{Target: "narrative.md", Section: "prologue", Rev: "abc", Content: "new"},
		{Target: "narrative.md", Section: "chapter_01", Rev: "def", Content: "more"},
	}

	p, err := ps.Stage("story-writer", edits)
	if err != nil {
		t.Fatal(err)
	}
	if p.ID == "" {
		t.Error("proposal should have an ID")
	}
	if p.GuestID != "story-writer" {
		t.Errorf("guestID = %q", p.GuestID)
	}
	if len(p.Edits) != 2 {
		t.Errorf("expected 2 edits, got %d", len(p.Edits))
	}
	for _, status := range p.Status {
		if status != "pending" {
			t.Errorf("all status should be pending, got %q", status)
		}
	}
}

func TestProposalStore_Accept(t *testing.T) {
	ps := NewProposalStore(10 * time.Minute)
	edits := []StoryEdit{
		{Target: "narrative.md", Section: "prologue", Rev: "abc", Content: "new prologue"},
	}

	p, _ := ps.Stage("story-writer", edits)

	edit, guestID, err := ps.Accept(p.ID, "prologue")
	if err != nil {
		t.Fatal(err)
	}
	if guestID != "story-writer" {
		t.Errorf("guestID = %q, want story-writer", guestID)
	}
	if edit.Content != "new prologue" {
		t.Errorf("edit content = %q", edit.Content)
	}
	if p.Status["prologue"] != "accepted" {
		t.Errorf("status = %q, want accepted", p.Status["prologue"])
	}
}

func TestProposalStore_AcceptIdempotent(t *testing.T) {
	ps := NewProposalStore(10 * time.Minute)
	edits := []StoryEdit{
		{Target: "narrative.md", Section: "prologue", Rev: "abc", Content: "text"},
	}
	p, _ := ps.Stage("writer", edits)

	_, _, err1 := ps.Accept(p.ID, "prologue")
	if err1 != nil {
		t.Fatal(err1)
	}
	_, _, err2 := ps.Accept(p.ID, "prologue")
	if err2 != nil {
		t.Errorf("second accept should be idempotent, got %v", err2)
	}
}

func TestProposalStore_Reject(t *testing.T) {
	ps := NewProposalStore(10 * time.Minute)
	edits := []StoryEdit{
		{Target: "narrative.md", Section: "prologue", Rev: "abc", Content: "text"},
	}
	p, _ := ps.Stage("writer", edits)

	if err := ps.Reject(p.ID, "prologue"); err != nil {
		t.Fatal(err)
	}
	if p.Status["prologue"] != "rejected" {
		t.Errorf("status = %q, want rejected", p.Status["prologue"])
	}
}

func TestProposalStore_NotFound(t *testing.T) {
	ps := NewProposalStore(10 * time.Minute)

	_, _, err := ps.Accept("nonexistent", "sec")
	if err != ErrProposalNotFound {
		t.Errorf("expected ErrProposalNotFound, got %v", err)
	}

	if err := ps.Reject("nonexistent", "sec"); err != ErrProposalNotFound {
		t.Errorf("expected ErrProposalNotFound, got %v", err)
	}
}

func TestProposalStore_SectionNotFound(t *testing.T) {
	ps := NewProposalStore(10 * time.Minute)
	edits := []StoryEdit{
		{Target: "narrative.md", Section: "prologue", Rev: "abc", Content: "text"},
	}
	p, _ := ps.Stage("writer", edits)

	_, _, err := ps.Accept(p.ID, "nonexistent_section")
	if err != ErrSectionNotFound {
		t.Errorf("expected ErrSectionNotFound, got %v", err)
	}
}

func TestProposalStore_Expire(t *testing.T) {
	ps := NewProposalStore(1 * time.Millisecond)
	edits := []StoryEdit{
		{Target: "narrative.md", Section: "prologue", Rev: "abc", Content: "text"},
	}
	p, _ := ps.Stage("writer", edits)

	time.Sleep(5 * time.Millisecond)
	ps.Expire()

	_, ok := ps.Get(p.ID)
	if ok {
		t.Error("expired proposal should be gone")
	}
}

func TestProposalStore_Get(t *testing.T) {
	ps := NewProposalStore(10 * time.Minute)
	edits := []StoryEdit{
		{Target: "narrative.md", Section: "prologue", Rev: "abc", Content: "text"},
	}
	p, _ := ps.Stage("writer", edits)

	got, ok := ps.Get(p.ID)
	if !ok {
		t.Fatal("proposal should exist")
	}
	if got.ID != p.ID {
		t.Errorf("ID mismatch")
	}

	_, ok = ps.Get("nonexistent")
	if ok {
		t.Error("nonexistent proposal should not be found")
	}
}

func TestProposalStore_ConcurrentAccess(t *testing.T) {
	ps := NewProposalStore(10 * time.Minute)

	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func() {
			edits := []StoryEdit{
				{Target: "narrative.md", Section: "prologue", Rev: "abc", Content: "text"},
			}
			_, _ = ps.Stage("writer", edits)
			done <- true
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
}
