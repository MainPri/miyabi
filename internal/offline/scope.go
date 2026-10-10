package offline

import (
	"entgo.io/ent/dialect/sql"
	"github.com/ppxb/miyabi/internal/domain"
	"github.com/ppxb/miyabi/internal/ent"
	"github.com/ppxb/miyabi/internal/ent/offlinedownload"
	"github.com/ppxb/miyabi/internal/ent/predicate"
)

// recordMounted reports whether a download record is pinned to a currently
// mounted root of its account.
func recordMounted(record *ent.OfflineDownload, sources []domain.LibrarySource) bool {
	for _, source := range sources {
		if source.AccountID == record.AccountID && source.Directory.ID == record.DirectoryID {
			return true
		}
	}
	return false
}

// recordSource returns the mounted source a download record is pinned to.
func recordSource(record *ent.OfflineDownload, sources []domain.LibrarySource) *domain.LibrarySource {
	for _, source := range sources {
		if source.AccountID == record.AccountID && source.Directory.ID == record.DirectoryID {
			value := source
			return &value
		}
	}
	return nil
}

// mountedRoots narrows the mount set to one account's directory IDs.
func mountedRoots(sources []domain.LibrarySource, accountID string) []string {
	roots := make([]string, 0, len(sources))
	for _, source := range sources {
		if source.AccountID == accountID {
			roots = append(roots, source.Directory.ID)
		}
	}
	return roots
}

// accountDownloads scopes download queries to records pinned to a mounted root.
func accountDownloads(sources []domain.LibrarySource, accountID string) predicate.OfflineDownload {
	roots := mountedRoots(sources, accountID)
	if len(roots) == 0 {
		return func(s *sql.Selector) { s.Where(sql.False()) }
	}
	return offlinedownload.And(offlinedownload.AccountIDEQ(accountID), offlinedownload.DirectoryIDIn(roots...))
}
