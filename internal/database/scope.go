package database

import (
	"entgo.io/ent/dialect/sql"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent/file"
	"github.com/ppxb/miyabi/internal/ent/predicate"
)

// LibraryFiles scopes file rows to the account and root directory of a mounted source.
func LibraryFiles(source domain.LibrarySource) predicate.File {
	return file.And(file.AccountIDEQ(source.AccountID), file.RootIDEQ(source.Directory.ID))
}

// AccountFiles scopes file rows to an account across the given root directories.
func AccountFiles(accountID string, rootIDs []string) predicate.File {
	roots := make([]predicate.File, 0, len(rootIDs))
	for _, id := range rootIDs {
		roots = append(roots, file.RootIDEQ(id))
	}
	if len(roots) == 0 {
		return func(s *sql.Selector) { s.Where(sql.False()) }
	}
	return file.And(file.AccountIDEQ(accountID), file.Or(roots...))
}
