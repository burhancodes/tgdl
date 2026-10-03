package jobs

import (
	"context"
	"strconv"
	"sync"
	"time"
)

// Choice values for interactive prompts.
const (
	ChoiceArchiveOnly = "only"
	ChoiceExtract     = "ext"
	ChoiceMP3         = "mp3"
	ChoiceOriginal    = "orig"
)

// prompt is a single-shot decision awaited by the pipeline.
type prompt struct {
	once   sync.Once
	done   chan struct{}
	choice string
}

func newPrompt() *prompt { return &prompt{done: make(chan struct{})} }

func (p *prompt) resolve(choice string) {
	p.once.Do(func() { p.choice = choice; close(p.done) })
}

// wait blocks until the prompt is resolved, the timeout elapses, or ctx ends.
// It returns the choice and whether one was made.
func (p *prompt) wait(ctx context.Context, timeout time.Duration) (string, bool) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-p.done:
		return p.choice, true
	case <-t.C:
		return "", false
	case <-ctx.Done():
		return "", false
	}
}

// sessions holds all interactive per-job decisions. It lives inside State so
// it is created and dropped with the job — no global registries to leak.
type sessions struct {
	mu sync.Mutex

	nextArchive int
	archiveRel  map[string]string // id -> relative path
	archiveDone map[string]string // id -> choice
	archiveWait map[string]*prompt
	extracted   map[string]struct{} // relative paths already extracted

	nextConv     int
	convID       map[string]string // id -> filename
	convertedSet map[string]struct{}
	// audioChoice is sticky: once answered it applies to later audio files.
	audioChoice string
	convWait    map[string]*prompt

	passwordWait map[int]*passwordPrompt // prompt message id -> waiter
}

type passwordPrompt struct {
	ArchiveID string
	ch        chan string
}

func newSessions() *sessions {
	return &sessions{
		archiveRel: map[string]string{}, archiveDone: map[string]string{}, archiveWait: map[string]*prompt{},
		extracted: map[string]struct{}{}, convID: map[string]string{}, convertedSet: map[string]struct{}{},
		convWait: map[string]*prompt{}, passwordWait: map[int]*passwordPrompt{},
	}
}

// archiveID returns the stable id for an archive path, allocating on first use.
func (s *sessions) archiveID(rel string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, r := range s.archiveRel {
		if r == rel {
			return id
		}
	}
	s.nextArchive++
	id := strconv.Itoa(s.nextArchive)
	s.archiveRel[id] = rel
	return id
}

func (s *sessions) archiveChoice(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.archiveDone[id]
	return c, ok
}

func (s *sessions) setArchiveChoice(id, choice string) {
	s.mu.Lock()
	s.archiveDone[id] = choice
	p := s.archiveWait[id]
	s.mu.Unlock()
	if p != nil {
		p.resolve(choice)
	}
}

func (s *sessions) archivePrompt(id string) *prompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := newPrompt()
	s.archiveWait[id] = p
	return p
}

func (s *sessions) archiveName(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.archiveRel[id]
}

func (s *sessions) markExtracted(rel string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.extracted[rel]; ok {
		return false
	}
	s.extracted[rel] = struct{}{}
	return true
}

func (s *sessions) convertedBefore(name string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.convertedSet[name]
	return ok
}

func (s *sessions) markConverted(name string) {
	s.mu.Lock()
	s.convertedSet[name] = struct{}{}
	s.mu.Unlock()
}

func (s *sessions) convID2(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, n := range s.convID {
		if n == name {
			return id
		}
	}
	s.nextConv++
	id := strconv.Itoa(s.nextConv)
	s.convID[id] = name
	return id
}

func (s *sessions) convName(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.convID[id]
}

func (s *sessions) audioSticky() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.audioChoice
}

func (s *sessions) convPrompt(id string) *prompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := newPrompt()
	s.convWait[id] = p
	return p
}

func (s *sessions) setAudioChoice(id, choice string) {
	s.mu.Lock()
	s.audioChoice = choice
	p := s.convWait[id]
	s.mu.Unlock()
	if p != nil {
		p.resolve(choice)
	}
}

func (s *sessions) newPasswordWait(msgID int, archiveID string) *passwordPrompt {
	s.mu.Lock()
	defer s.mu.Unlock()
	pp := &passwordPrompt{ArchiveID: archiveID, ch: make(chan string, 1)}
	s.passwordWait[msgID] = pp
	return pp
}

func (s *sessions) dropPasswordWait(msgID int) {
	s.mu.Lock()
	delete(s.passwordWait, msgID)
	s.mu.Unlock()
}

func (s *sessions) deliverPassword(msgID int, pw string) bool {
	s.mu.Lock()
	pp := s.passwordWait[msgID]
	s.mu.Unlock()
	if pp == nil {
		return false
	}
	select {
	case pp.ch <- pw:
		return true
	default:
		return false
	}
}
