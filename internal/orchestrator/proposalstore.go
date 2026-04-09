package orchestrator

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

var (
	ErrProposalNotFound = errors.New("proposal not found")
	ErrSectionNotFound  = errors.New("section not found in proposal")
)

type Proposal struct {
	ID        string
	GuestID   string
	Edits     []StoryEdit
	CreatedAt time.Time
	Status    map[string]string // sectionID -> "pending" | "accepted" | "rejected"
}

type ProposalStore struct {
	mu        sync.Mutex
	proposals map[string]*Proposal
	ttl       time.Duration
}

func NewProposalStore(ttl time.Duration) *ProposalStore {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	return &ProposalStore{
		proposals: make(map[string]*Proposal),
		ttl:       ttl,
	}
}

func (ps *ProposalStore) Stage(guestID string, edits []StoryEdit) (*Proposal, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	id := newProposalID()
	status := make(map[string]string, len(edits))
	for _, e := range edits {
		key := e.Section
		if key == "" {
			key = e.After
		}
		if key == "" {
			key = "_append_" + id
		}
		status[key] = "pending"
	}

	p := &Proposal{
		ID:        id,
		GuestID:   guestID,
		Edits:     edits,
		CreatedAt: time.Now(),
		Status:    status,
	}
	ps.proposals[id] = p
	return p, nil
}

func (ps *ProposalStore) Accept(proposalID, sectionID string) (*StoryEdit, string, error) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	p, ok := ps.proposals[proposalID]
	if !ok {
		return nil, "", ErrProposalNotFound
	}

	st, ok := p.Status[sectionID]
	if !ok {
		return nil, "", ErrSectionNotFound
	}
	if st == "accepted" {
		for i := range p.Edits {
			if p.Edits[i].Section == sectionID || p.Edits[i].After == sectionID {
				return &p.Edits[i], p.GuestID, nil
			}
		}
		return nil, "", ErrSectionNotFound
	}

	p.Status[sectionID] = "accepted"

	for i := range p.Edits {
		if p.Edits[i].Section == sectionID || p.Edits[i].After == sectionID {
			return &p.Edits[i], p.GuestID, nil
		}
		key := p.Edits[i].Section
		if key == "" {
			key = p.Edits[i].After
		}
		if key == sectionID {
			return &p.Edits[i], p.GuestID, nil
		}
	}

	return nil, "", ErrSectionNotFound
}

func (ps *ProposalStore) Reject(proposalID, sectionID string) error {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	p, ok := ps.proposals[proposalID]
	if !ok {
		return ErrProposalNotFound
	}

	if _, ok := p.Status[sectionID]; !ok {
		return ErrSectionNotFound
	}
	p.Status[sectionID] = "rejected"
	return nil
}

func (ps *ProposalStore) Get(proposalID string) (*Proposal, bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	p, ok := ps.proposals[proposalID]
	return p, ok
}

func (ps *ProposalStore) Expire() {
	ps.mu.Lock()
	defer ps.mu.Unlock()

	cutoff := time.Now().Add(-ps.ttl)
	for id, p := range ps.proposals {
		if p.CreatedAt.Before(cutoff) {
			delete(ps.proposals, id)
		}
	}
}

func newProposalID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "p_" + hex.EncodeToString(b)
}
