package review

import (
	"context"

	"github.com/NeswaEA/IF3110_K01_G03/backend/internal/contracts"
)

// PendingRequests returns the IDs of a database's pending_review change requests; the migrations module owns them.
type PendingRequests func(ctx context.Context, databaseID string) ([]string, error)

type NamedRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// InboxEntry is a request that still waits for the actor's decision, with the groups that decision covers.
type InboxEntry struct {
	RequestID string
	Database  NamedRef
	MyGroups  []NamedRef
}

// Inbox lists, per database the actor leads a granted group of, the open requests where one of those groups
// has not decided yet. The required groups are evaluated live, exactly as approve does.
func (s *Service) Inbox(ctx context.Context, actor contracts.Principal, pending PendingRequests) ([]InboxEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT dc.id, dc.name
		FROM groups g
		JOIN group_database_access ga ON ga.group_id = g.id
		JOIN database_connections dc ON dc.id = ga.database_id AND dc.active
		WHERE g.leader_user_id = $1
		ORDER BY dc.name, dc.id`, actor.UserID)
	if err != nil {
		return nil, failure(err)
	}
	var databases []NamedRef
	for rows.Next() {
		var d NamedRef
		if err := rows.Scan(&d.ID, &d.Name); err != nil {
			rows.Close()
			return nil, failure(err)
		}
		databases = append(databases, d)
	}
	if err := rows.Close(); err != nil {
		return nil, failure(err)
	}
	if err := rows.Err(); err != nil {
		return nil, failure(err)
	}
	entries := []InboxEntry{}
	for _, d := range databases {
		ids, err := pending(ctx, d.ID)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			subject, err := s.subjects.LoadSubject(ctx, s.db, id)
			if err != nil {
				return nil, err
			}
			if subject.Status != "pending_review" || subject.DatabaseID != d.ID {
				continue // decided between the list and this read
			}
			state, err := s.Evaluate(ctx, s.db, subject)
			if err != nil {
				return nil, err
			}
			entry := InboxEntry{RequestID: id, Database: d, MyGroups: []NamedRef{}}
			for _, r := range state.Required {
				if r.LeaderID != nil && *r.LeaderID == actor.UserID && r.Decision == "pending" {
					entry.MyGroups = append(entry.MyGroups, NamedRef{ID: r.GroupID, Name: r.GroupName})
				}
			}
			if len(entry.MyGroups) > 0 {
				entries = append(entries, entry)
			}
		}
	}
	return entries, nil
}
