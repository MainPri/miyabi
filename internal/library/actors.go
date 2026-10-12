package library

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/ent/actor"
	"github.com/ppxb/miyabi/internal/ent/movie"
)

// Actor is a scraped performer aggregated over the movies currently indexed
// in the mounted library.
type Actor struct {
	ID         int     `json:"id"`
	Provider   string  `json:"provider"`
	SourceID   string  `json:"source_id"`
	Name       string  `json:"name"`
	NameZht    *string `json:"name_zht,omitempty"`
	Avatar     *string `json:"avatar,omitempty"`
	MovieCount int     `json:"movie_count"`
}

// ActorPage is one page of the library actor index.
type ActorPage struct {
	Actors  []Actor `json:"actors"`
	Total   int     `json:"total"`
	Page    int     `json:"page"`
	HasMore bool    `json:"has_more"`
}

// Actors lists female performers owning at least one movie indexed in the
// mounted library, ordered by movie count then name. Counts share the movie
// listing scope, so index changes move actors together with their movies.
func (s *Service) Actors(ctx context.Context, page, limit int) (ActorPage, error) {
	result := ActorPage{Actors: []Actor{}, Page: page}
	scope := libraryScope(s.Sources())
	records, err := s.database.Actor.Query().
		Where(actor.GenderEQ(actor.GenderFemale), actor.HasMoviesWith(movie.HasFilesWith(scope))).
		Select(actor.FieldID, actor.FieldProvider, actor.FieldSourceID, actor.FieldName, actor.FieldNameZht, actor.FieldAvatar).
		WithMovies(func(query *ent.MovieQuery) {
			query.Where(movie.HasFilesWith(scope)).Select(movie.FieldID)
		}).
		All(ctx)
	if err != nil {
		return result, fmt.Errorf("list library actors: %w", err)
	}
	items := make([]Actor, 0, len(records))
	for _, record := range records {
		items = append(items, Actor{
			ID: record.ID, Provider: record.Provider, SourceID: record.SourceID, Name: record.Name,
			NameZht: record.NameZht, Avatar: record.Avatar, MovieCount: len(record.Edges.Movies),
		})
	}
	slices.SortFunc(items, func(a, b Actor) int {
		if c := cmp.Compare(b.MovieCount, a.MovieCount); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	result.Total = len(items)
	if start := (page - 1) * limit; start < len(items) {
		end := min(start+limit, len(items))
		result.Actors = items[start:end]
		result.HasMore = end < len(items)
	}
	return result, nil
}
