// SPDX-License-Identifier: AGPL-3.0-only
package publisherrating

import (
	"context"
	s "solsynth.local/go-migration/ports/sphere/sponsorship"
	"time"
)

const Policy = "reaction_only_v1"

type Overview struct {
	Policy             string  `json:"policy"`
	AwardScoreIncluded bool    `json:"award_score_included"`
	Rating             int64   `json:"rating"`
	Rank               int     `json:"rank"`
	TotalPublishers    int     `json:"total_publishers"`
	Percentile         float64 `json:"percentile"`
	Grade              string  `json:"grade"`
}
type Entry struct {
	Policy             string  `json:"policy"`
	AwardScoreIncluded bool    `json:"award_score_included"`
	PublisherID        string  `json:"publisher_id"`
	Name               string  `json:"name"`
	Nick               string  `json:"nick"`
	Picture            any     `json:"picture"`
	Rating             int64   `json:"rating"`
	Rank               int     `json:"rank"`
	Percentile         float64 `json:"percentile"`
	Grade              string  `json:"grade"`
}
type Record struct {
	UpdatedAt   time.Time  `json:"updated_at"`
	DeletedAt   *time.Time `json:"deleted_at"`
	ID          string     `json:"id"`
	PublisherID string     `json:"publisher_id"`
	ReasonType  string     `json:"reason_type"`
	Reason      string     `json:"reason"`
	Delta       int64      `json:"delta"`
	CreatedAt   time.Time  `json:"created_at"`
	PeriodStart time.Time  `json:"period_start"`
	Policy      string     `json:"policy"`
}
type History struct {
	IsMember bool     `json:"is_member"`
	Records  []Record `json:"records"`
}
type Repository interface {
	Rating(context.Context, string, string) (int64, error)
	Overview(context.Context, string, string) (Overview, error)
	Leaderboard(context.Context, string, s.Page) ([]Entry, int, error)
	History(context.Context, string, string, s.Page) (History, int, error)
}

func Grade(percentile float64, rank int) string {
	if rank == 1 {
		return "S++"
	}
	for _, v := range []struct {
		n float64
		s string
	}{{99, "S+"}, {95, "S"}, {90, "A++"}, {80, "A+"}, {70, "A"}, {60, "A-"}, {50, "B+"}, {40, "B"}, {20, "C"}} {
		if percentile >= v.n {
			return v.s
		}
	}
	return "D"
}
