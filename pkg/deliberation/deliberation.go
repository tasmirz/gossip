package deliberation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tasmirz/gossip/pkg/state"
)

// Stance constants
const (
	StancePro      = "pro"
	StanceCon      = "con"
	StanceCritique = "critique"
	StanceRebuttal = "rebuttal"
	StanceNeutral  = "neutral"
)

// DebateStatus constants
const (
	DebateStatusOpen         = "open"
	DebateStatusDeliberating = "deliberating"
	DebateStatusVoting       = "voting"
	DebateStatusResolved     = "resolved"
	DebateStatusRejected     = "rejected"
)

// ReviewStatus constants
const (
	ReviewStatusPending          = "pending"
	ReviewStatusInReview         = "in_review"
	ReviewStatusApproved         = "approved"
	ReviewStatusChangesRequested = "changes_requested"
	ReviewStatusRejected         = "rejected"
)

// ProbeStatus constants
const (
	ProbeStatusVulnerable    = "vulnerable"
	ProbeStatusMitigated     = "mitigated"
	ProbeStatusFalsePositive = "false_positive"
)

// Argument represents an individual turn in a multi-agent debate.
type Argument struct {
	ID        string    `json:"id"`
	AgentID   string    `json:"agent_id"`
	Role      string    `json:"role"`
	Stance    string    `json:"stance"` // pro, con, critique, rebuttal, neutral
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// DebateRound groups arguments in an iterative debate turn.
type DebateRound struct {
	RoundNumber int        `json:"round_number"`
	Arguments   []Argument `json:"arguments"`
}

// Vote records an agent's stance on a debate proposal.
type Vote struct {
	AgentID    string    `json:"agent_id"`
	Role       string    `json:"role"`
	Choice     string    `json:"choice"` // accept, reject, abstain
	Confidence int       `json:"confidence"` // 1 to 5
	Reasoning  string    `json:"reasoning"`
	CreatedAt  time.Time `json:"created_at"`
}

// Resolution captures the finalized consensus of a debate.
type Resolution struct {
	Outcome         string    `json:"outcome"` // accepted, rejected, tied
	Summary         string    `json:"summary"`
	AdoptedDecision string    `json:"adopted_decision,omitempty"`
	ResolvedBy      string    `json:"resolved_by"`
	ResolvedAt      time.Time `json:"resolved_at"`
}

// Debate manages an asynchronous multi-agent deliberation.
type Debate struct {
	ID               string                     `json:"id"`
	Title            string                     `json:"title"`
	Description      string                     `json:"description"`
	Proposer         string                     `json:"proposer"`
	Role             string                     `json:"role"`
	Status           string                     `json:"status"`
	Options          []string                   `json:"options,omitempty"`
	Rounds           []DebateRound              `json:"rounds"`
	Votes            map[string]Vote            `json:"votes"`
	MultiOptionVotes map[string]MultiOptionVote `json:"multi_option_votes,omitempty"`
	Resolution       *Resolution                `json:"resolution,omitempty"`
	CreatedAt        time.Time                  `json:"created_at"`
	UpdatedAt        time.Time                  `json:"updated_at"`
}

// Finding represents a discrete critique item in a peer review.
type Finding struct {
	ID             string `json:"id"`
	Severity       string `json:"severity"` // info, minor, major, critical
	Category       string `json:"category"` // logic, security, performance, concurrency, style
	Location       string `json:"location,omitempty"`
	Description    string `json:"description"`
	Recommendation string `json:"recommendation"`
}

// Review models a formal peer review or critique cycle.
type Review struct {
	ID         string    `json:"id"`
	TargetType string    `json:"target_type"` // task, file, artifact, decision
	TargetID   string    `json:"target_id"`
	AuthorID   string    `json:"author_id"`
	ReviewerID string    `json:"reviewer_id"`
	Status     string    `json:"status"`
	Findings   []Finding `json:"findings"`
	Verdict    string    `json:"verdict,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// RedTeamProbe models an adversarial penetration or fuzzing test vector.
type RedTeamProbe struct {
	ID                 string    `json:"id"`
	Target             string    `json:"target"`
	ThreatCategory     string    `json:"threat_category"` // STRIDE category or concurrency_race, boundary_overflow
	AttackerID         string    `json:"attacker_id"`
	AttackVector       string    `json:"attack_vector"`
	PoCPayload         string    `json:"poc_payload,omitempty"`
	Status             string    `json:"status"` // vulnerable, mitigated, false_positive
	RemediationTaskID  string    `json:"remediation_task_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// DeliberationStore holds all debates, reviews, and red-team probes in deliberations.json.
type DeliberationStore struct {
	Version string                  `json:"version"`
	Debates map[string]Debate       `json:"debates"`
	Reviews map[string]Review       `json:"reviews"`
	Probes  map[string]RedTeamProbe `json:"probes"`
}

// Manager coordinates debates, peer reviews, and red team adversarial testing.
type Manager struct {
	store *state.Store
}

// NewManager creates a deliberation manager backed by state.Store.
func NewManager(store *state.Store) *Manager {
	return &Manager{store: store}
}

func (m *Manager) filePath() string {
	return filepath.Join(m.store.Dir(), "deliberations.json")
}

// Load loads deliberations.json or initializes a clean container.
func (m *Manager) Load() (*DeliberationStore, error) {
	data, err := os.ReadFile(m.filePath())
	if err != nil {
		if os.IsNotExist(err) {
			return &DeliberationStore{
				Version: "1.0.0",
				Debates: make(map[string]Debate),
				Reviews: make(map[string]Review),
				Probes:  make(map[string]RedTeamProbe),
			}, nil
		}
		return nil, err
	}

	var ds DeliberationStore
	if err := json.Unmarshal(data, &ds); err != nil {
		return nil, fmt.Errorf("corrupted deliberations.json: %w", err)
	}
	if ds.Debates == nil {
		ds.Debates = make(map[string]Debate)
	}
	if ds.Reviews == nil {
		ds.Reviews = make(map[string]Review)
	}
	if ds.Probes == nil {
		ds.Probes = make(map[string]RedTeamProbe)
	}
	return &ds, nil
}

// Save atomically writes deliberations.json.
func (m *Manager) Save(ds *DeliberationStore) error {
	data, err := json.MarshalIndent(ds, "", "  ")
	if err != nil {
		return err
	}
	return m.store.AtomicWriteFile("deliberations.json", data)
}

// StartDebate opens a new deliberation session for agents to debate a proposal.
func (m *Manager) StartDebate(title, description, proposer, role string) (*Debate, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	debateID := fmt.Sprintf("deb-%d", now.UnixNano()/1000000)

	deb := Debate{
		ID:          debateID,
		Title:       title,
		Description: description,
		Proposer:    proposer,
		Role:        role,
		Status:      DebateStatusOpen,
		Rounds: []DebateRound{
			{
				RoundNumber: 1,
				Arguments:   []Argument{},
			},
		},
		Votes:            make(map[string]Vote),
		MultiOptionVotes: make(map[string]MultiOptionVote),
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	ds.Debates[debateID] = deb
	if err := m.Save(ds); err != nil {
		return nil, err
	}

	return &deb, nil
}

// PostArgument contributes a pro, con, critique, or rebuttal to the active debate round.
func (m *Manager) PostArgument(debateID, agentID, role, stance, content string) (*Argument, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	deb, exists := ds.Debates[debateID]
	if !exists {
		return nil, fmt.Errorf("debate %s not found", debateID)
	}

	if deb.Status == DebateStatusResolved || deb.Status == DebateStatusRejected {
		return nil, fmt.Errorf("debate %s is already concluded", debateID)
	}

	now := time.Now()
	argID := fmt.Sprintf("arg-%d", now.UnixNano()/1000000)
	arg := Argument{
		ID:        argID,
		AgentID:   agentID,
		Role:      role,
		Stance:    stance,
		Content:   content,
		CreatedAt: now,
	}

	if len(deb.Rounds) == 0 {
		deb.Rounds = append(deb.Rounds, DebateRound{RoundNumber: 1, Arguments: []Argument{}})
	}

	currentRound := len(deb.Rounds) - 1
	deb.Rounds[currentRound].Arguments = append(deb.Rounds[currentRound].Arguments, arg)
	deb.Status = DebateStatusDeliberating
	deb.UpdatedAt = now

	ds.Debates[debateID] = deb
	if err := m.Save(ds); err != nil {
		return nil, err
	}

	return &arg, nil
}

// AdvanceRound closes the current round and opens the next round for critique and revision.
func (m *Manager) AdvanceRound(debateID string) error {
	unlock, err := m.store.Lock()
	if err != nil {
		return err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return err
	}

	deb, exists := ds.Debates[debateID]
	if !exists {
		return fmt.Errorf("debate %s not found", debateID)
	}

	nextRoundNum := len(deb.Rounds) + 1
	deb.Rounds = append(deb.Rounds, DebateRound{
		RoundNumber: nextRoundNum,
		Arguments:   []Argument{},
	})
	deb.UpdatedAt = time.Now()
	ds.Debates[debateID] = deb

	return m.Save(ds)
}

// CastVote records an agent's formal vote on a debate proposal.
func (m *Manager) CastVote(debateID, agentID, role, choice string, confidence int, reasoning string) (*Vote, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	deb, exists := ds.Debates[debateID]
	if !exists {
		return nil, fmt.Errorf("debate %s not found", debateID)
	}

	if confidence < 1 {
		confidence = 1
	}
	if confidence > 5 {
		confidence = 5
	}

	vote := Vote{
		AgentID:    agentID,
		Role:       role,
		Choice:     strings.ToLower(choice),
		Confidence: confidence,
		Reasoning:  reasoning,
		CreatedAt:  time.Now(),
	}

	if deb.Votes == nil {
		deb.Votes = make(map[string]Vote)
	}
	deb.Votes[agentID] = vote
	deb.Status = DebateStatusVoting
	deb.UpdatedAt = time.Now()

	ds.Debates[debateID] = deb
	if err := m.Save(ds); err != nil {
		return nil, err
	}

	return &vote, nil
}

// ConcludeDebate tallies votes and declares the final consensus outcome.
func (m *Manager) ConcludeDebate(debateID, resolvedBy, summary, adoptedDecision string) (*Resolution, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	deb, exists := ds.Debates[debateID]
	if !exists {
		return nil, fmt.Errorf("debate %s not found", debateID)
	}

	acceptCount := 0
	rejectCount := 0
	for _, v := range deb.Votes {
		if v.Choice == "accept" || v.Choice == "yes" || v.Choice == "pro" {
			acceptCount++
		} else if v.Choice == "reject" || v.Choice == "no" || v.Choice == "con" {
			rejectCount++
		}
	}

	outcome := "accepted"
	if rejectCount > acceptCount {
		outcome = "rejected"
		deb.Status = DebateStatusRejected
	} else if rejectCount == acceptCount && len(deb.Votes) > 0 {
		outcome = "tied"
		deb.Status = DebateStatusResolved
	} else {
		deb.Status = DebateStatusResolved
	}

	res := Resolution{
		Outcome:         outcome,
		Summary:         summary,
		AdoptedDecision: adoptedDecision,
		ResolvedBy:      resolvedBy,
		ResolvedAt:      time.Now(),
	}

	deb.Resolution = &res
	deb.UpdatedAt = time.Now()
	ds.Debates[debateID] = deb

	if err := m.Save(ds); err != nil {
		return nil, err
	}

	return &res, nil
}

// RequestReview submits a task or file for formal peer critique.
func (m *Manager) RequestReview(targetType, targetID, authorID, reviewerID string) (*Review, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	reviewID := fmt.Sprintf("rev-%d", now.UnixNano()/1000000)

	rev := Review{
		ID:         reviewID,
		TargetType: targetType,
		TargetID:   targetID,
		AuthorID:   authorID,
		ReviewerID: reviewerID,
		Status:     ReviewStatusPending,
		Findings:   []Finding{},
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	ds.Reviews[reviewID] = rev
	if err := m.Save(ds); err != nil {
		return nil, err
	}

	return &rev, nil
}

// SubmitReview records critique findings and issues a formal review verdict.
func (m *Manager) SubmitReview(reviewID, reviewerID, status, verdict string, findings []Finding) (*Review, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	rev, exists := ds.Reviews[reviewID]
	if !exists {
		return nil, fmt.Errorf("review %s not found", reviewID)
	}

	rev.ReviewerID = reviewerID
	rev.Status = status
	rev.Verdict = verdict
	rev.Findings = append(rev.Findings, findings...)
	rev.UpdatedAt = time.Now()

	ds.Reviews[reviewID] = rev
	if err := m.Save(ds); err != nil {
		return nil, err
	}

	return &rev, nil
}

// LogRedTeamProbe records an adversarial attack scenario and links remediation.
func (m *Manager) LogRedTeamProbe(target, threatCategory, attackerID, attackVector, pocPayload, status, remediationTaskID string) (*RedTeamProbe, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	probeID := fmt.Sprintf("probe-%d", now.UnixNano()/1000000)

	if status == "" {
		status = ProbeStatusVulnerable
	}

	probe := RedTeamProbe{
		ID:                probeID,
		Target:            target,
		ThreatCategory:    threatCategory,
		AttackerID:        attackerID,
		AttackVector:      attackVector,
		PoCPayload:        pocPayload,
		Status:            status,
		RemediationTaskID: remediationTaskID,
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	ds.Probes[probeID] = probe
	if err := m.Save(ds); err != nil {
		return nil, err
	}

	return &probe, nil
}

// ListDebates returns all debates filtered by status.
func (m *Manager) ListDebates(statusFilter string) ([]Debate, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	var list []Debate
	for _, d := range ds.Debates {
		if statusFilter != "" && !strings.EqualFold(d.Status, statusFilter) {
			continue
		}
		list = append(list, d)
	}
	return list, nil
}

// ListReviews returns peer reviews with optional status filter.
func (m *Manager) ListReviews(statusFilter string) ([]Review, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	var list []Review
	for _, r := range ds.Reviews {
		if statusFilter != "" && !strings.EqualFold(r.Status, statusFilter) {
			continue
		}
		list = append(list, r)
	}
	return list, nil
}

// ListProbes returns red-team security probes with optional status filter.
func (m *Manager) ListProbes(statusFilter string) ([]RedTeamProbe, error) {
	unlock, err := m.store.Lock()
	if err != nil {
		return nil, err
	}
	defer unlock()

	ds, err := m.Load()
	if err != nil {
		return nil, err
	}

	var list []RedTeamProbe
	for _, p := range ds.Probes {
		if statusFilter != "" && !strings.EqualFold(p.Status, statusFilter) {
			continue
		}
		list = append(list, p)
	}
	return list, nil
}
