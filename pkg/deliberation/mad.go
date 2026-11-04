package deliberation

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// MultiOptionVote records an agent's ranked preferences on a multi-candidate decision.
type MultiOptionVote struct {
	AgentID       string    `json:"agent_id"`
	Role          string    `json:"role"`
	RankedChoices []string  `json:"ranked_choices"` // ordered from most preferred to least preferred
	Confidence    int       `json:"confidence"`     // 1 to 5
	Reasoning     string    `json:"reasoning"`
	CreatedAt     time.Time `json:"created_at"`
}

// OptionScore holds the calculated Borda score for an option.
type OptionScore struct {
	Option          string  `json:"option"`
	BordaScore      float64 `json:"borda_score"`
	FirstPlaceVotes int     `json:"first_place_votes"`
	Rank            int     `json:"rank"`
}

// RankedVotingResult contains the outcome of a Borda count or ranked-choice tally.
type RankedVotingResult struct {
	DebateID    string        `json:"debate_id"`
	Winner      string        `json:"winner"`
	TotalVoters int           `json:"total_voters"`
	Scores      []OptionScore `json:"scores"`
	Details     string        `json:"details"`
}

// RoundStanceStats records stance distribution per debate round.
type RoundStanceStats struct {
	RoundNumber  int            `json:"round_number"`
	StanceCounts map[string]int `json:"stance_counts"`
	Participants []string       `json:"participants"`
}

// ConvergenceReport evaluates whether a multi-round debate has reached consensus (inspired by Du et al. MAD).
type ConvergenceReport struct {
	DebateID       string             `json:"debate_id"`
	TotalRounds    int                `json:"total_rounds"`
	TotalArguments int                `json:"total_arguments"`
	CurrentRound   int                `json:"current_round"`
	AgreementRatio float64            `json:"agreement_ratio"` // 0.0 - 1.0 fraction of majority stance
	StabilityScore float64            `json:"stability_score"` // 0.0 - 1.0 fraction of agents maintaining position
	DominantStance string             `json:"dominant_stance"`
	HasConverged   bool               `json:"has_converged"`
	Recommendation string             `json:"recommendation"` // "proceed_to_vote", "continue_debate", "deadlock_detected"
	RoundSummaries []RoundStanceStats `json:"round_summaries"`
}

// CastRankedVote records an agent's ranked preferences among multi-choice options.
func (m *Manager) CastRankedVote(debateID, agentID, role string, rankedChoices []string, confidence int, reasoning string) error {
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
		return fmt.Errorf("debate %q not found", debateID)
	}

	if deb.MultiOptionVotes == nil {
		deb.MultiOptionVotes = make(map[string]MultiOptionVote)
	}

	if confidence < 1 {
		confidence = 1
	}
	if confidence > 5 {
		confidence = 5
	}

	vote := MultiOptionVote{
		AgentID:       agentID,
		Role:          role,
		RankedChoices: rankedChoices,
		Confidence:    confidence,
		Reasoning:     reasoning,
		CreatedAt:     time.Now(),
	}

	deb.MultiOptionVotes[agentID] = vote
	deb.UpdatedAt = time.Now()
	ds.Debates[debateID] = deb

	return m.Save(ds)
}

// TallyRankedVotes calculates consensus using confidence-weighted Borda Count.
// For K candidates:
// 1st place receives K points, 2nd receives K-1, ..., K-th receives 1 point.
// Point value is scaled by agent confidence multiplier (1.0 to 1.8).
func (m *Manager) TallyRankedVotes(debateID string) (*RankedVotingResult, error) {
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
		return nil, fmt.Errorf("debate %q not found", debateID)
	}

	if len(deb.MultiOptionVotes) == 0 {
		return nil, fmt.Errorf("no ranked votes cast yet in debate %q", debateID)
	}

	// Gather distinct options
	optionSet := make(map[string]bool)
	for _, opt := range deb.Options {
		if strings.TrimSpace(opt) != "" {
			optionSet[strings.TrimSpace(opt)] = true
		}
	}
	for _, vote := range deb.MultiOptionVotes {
		for _, choice := range vote.RankedChoices {
			if strings.TrimSpace(choice) != "" {
				optionSet[strings.TrimSpace(choice)] = true
			}
		}
	}

	options := make([]string, 0, len(optionSet))
	for opt := range optionSet {
		options = append(options, opt)
	}
	sort.Strings(options)

	k := len(options)
	if k == 0 {
		return nil, fmt.Errorf("no candidate options identified for tally")
	}

	scores := make(map[string]float64)
	firstPlaceCounts := make(map[string]int)

	for _, vote := range deb.MultiOptionVotes {
		weight := 1.0 + 0.2*float64(vote.Confidence-1) // 1.0 to 1.8
		for rankIdx, choice := range vote.RankedChoices {
			choice = strings.TrimSpace(choice)
			if !optionSet[choice] {
				continue
			}
			points := float64(k - rankIdx)
			if points < 1.0 {
				points = 1.0
			}
			scores[choice] += points * weight
			if rankIdx == 0 {
				firstPlaceCounts[choice]++
			}
		}
	}

	var results []OptionScore
	for _, opt := range options {
		results = append(results, OptionScore{
			Option:          opt,
			BordaScore:      math.Round(scores[opt]*100) / 100,
			FirstPlaceVotes: firstPlaceCounts[opt],
		})
	}

	sort.Slice(results, func(i, j int) bool {
		if results[i].BordaScore == results[j].BordaScore {
			return results[i].FirstPlaceVotes > results[j].FirstPlaceVotes
		}
		return results[i].BordaScore > results[j].BordaScore
	})

	for i := range results {
		results[i].Rank = i + 1
	}

	winner := ""
	if len(results) > 0 {
		winner = results[0].Option
	}

	var detailBuilder strings.Builder
	detailBuilder.WriteString(fmt.Sprintf("Borda count across %d voters:\n", len(deb.MultiOptionVotes)))
	for _, r := range results {
		detailBuilder.WriteString(fmt.Sprintf("  #%d %s: Score %.2f (%d 1st-place votes)\n", r.Rank, r.Option, r.BordaScore, r.FirstPlaceVotes))
	}

	return &RankedVotingResult{
		DebateID:    debateID,
		Winner:      winner,
		TotalVoters: len(deb.MultiOptionVotes),
		Scores:      results,
		Details:     detailBuilder.String(),
	}, nil
}

// CheckConvergence analyzes stance distribution and stability across debate rounds.
// Detects whether agents have converged towards a consensus solution (Du et al., 2024).
func (m *Manager) CheckConvergence(debateID string) (*ConvergenceReport, error) {
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
		return nil, fmt.Errorf("debate %q not found", debateID)
	}

	totalRounds := len(deb.Rounds)
	if totalRounds == 0 {
		return &ConvergenceReport{
			DebateID:       debateID,
			Recommendation: "continue_debate",
		}, nil
	}

	totalArgs := 0
	var roundSummaries []RoundStanceStats
	agentLastStance := make(map[string]string)
	agentPrevStance := make(map[string]string)

	for roundIdx, r := range deb.Rounds {
		counts := make(map[string]int)
		var participants []string
		for _, arg := range r.Arguments {
			totalArgs++
			counts[arg.Stance]++
			participants = append(participants, arg.AgentID)

			if roundIdx == totalRounds-2 {
				agentPrevStance[arg.AgentID] = arg.Stance
			}
			if roundIdx == totalRounds-1 {
				agentLastStance[arg.AgentID] = arg.Stance
			}
		}
		roundSummaries = append(roundSummaries, RoundStanceStats{
			RoundNumber:  r.RoundNumber,
			StanceCounts: counts,
			Participants: participants,
		})
	}

	latestRound := deb.Rounds[totalRounds-1]
	latestCounts := roundSummaries[totalRounds-1].StanceCounts

	dominantStance := ""
	maxCount := 0
	totalLatestArgs := len(latestRound.Arguments)

	for stance, count := range latestCounts {
		if count > maxCount {
			maxCount = count
			dominantStance = stance
		}
	}

	agreementRatio := 0.0
	if totalLatestArgs > 0 {
		agreementRatio = float64(maxCount) / float64(totalLatestArgs)
	}

	// Calculate stability (did agents change mind in the last turn?)
	stableCount := 0
	totalTracked := 0
	for agent, currStance := range agentLastStance {
		if prevStance, ok := agentPrevStance[agent]; ok {
			totalTracked++
			if prevStance == currStance {
				stableCount++
			}
		}
	}

	stabilityScore := 1.0
	if totalTracked > 0 {
		stabilityScore = float64(stableCount) / float64(totalTracked)
	}

	hasConverged := false
	recommendation := "continue_debate"

	if agreementRatio >= 0.75 && totalLatestArgs >= 2 {
		// High agreement in the current round (agents aligned or converged)
		hasConverged = true
		recommendation = "proceed_to_vote"
	} else if totalRounds >= 2 {
		if agreementRatio >= 0.60 && stabilityScore >= 0.70 {
			hasConverged = true
			recommendation = "proceed_to_vote"
		} else if totalRounds >= 4 && stabilityScore >= 0.80 && agreementRatio < 0.60 {
			// Persistent disagreement without movement across rounds = Deadlock
			recommendation = "deadlock_detected"
		}
	} else if totalLatestArgs >= 3 && agreementRatio >= 0.70 {
		hasConverged = true
		recommendation = "proceed_to_vote"
	}

	return &ConvergenceReport{
		DebateID:       debateID,
		TotalRounds:    totalRounds,
		TotalArguments: totalArgs,
		CurrentRound:   totalRounds,
		AgreementRatio: math.Round(agreementRatio*100) / 100,
		StabilityScore: math.Round(stabilityScore*100) / 100,
		DominantStance: dominantStance,
		HasConverged:   hasConverged,
		Recommendation: recommendation,
		RoundSummaries: roundSummaries,
	}, nil
}
