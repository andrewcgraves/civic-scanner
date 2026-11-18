package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const MaxFeedItems = 50

type AgencyFeed struct {
	Agency Agency
	Items  []FeedItem
}

type FeedItem struct {
	EventID         uuid.UUID
	Type            EventType
	OccurredAt      time.Time
	MeetingID       uuid.UUID
	MeetingTitle    string
	MeetingStartsAt time.Time
	AgendaVersion   *int
	AgendaSummary   *string
	AgendaChange    *string
}

const agencyFeedSQL = `
SELECT a.id, a.slug, a.name, a.website, a.created_at,
       e.id, e.type, e.occurred_at, e.meeting_id, e.title, e.starts_at,
       av.version, av.summary, av.change_note
FROM agencies a
LEFT JOIN LATERAL (
    SELECT ev.id, ev.sequence, ev.type, ev.occurred_at, ev.meeting_id, ev.payload, m.title, m.starts_at
    FROM meetings m
    CROSS JOIN LATERAL (
        SELECT id, sequence, type, occurred_at, meeting_id, payload
        FROM meeting_events
        WHERE meeting_id = m.id
        ORDER BY sequence DESC
        LIMIT $2
    ) ev
    WHERE m.agency_id = a.id
    ORDER BY ev.sequence DESC
    LIMIT $2
) e ON true
LEFT JOIN agenda_versions av
    ON e.type IN ('agenda_published', 'agenda_changed')
   AND av.meeting_id = e.meeting_id
   AND av.id = CASE
       WHEN e.payload->>'agenda_version_id' ~ '^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$'
       THEN (e.payload->>'agenda_version_id')::uuid
   END
WHERE a.slug = $1 AND a.status = 'live'
ORDER BY e.sequence DESC`

func (s *Store) GetLiveAgencyFeed(ctx context.Context, slug string) (AgencyFeed, error) {
	rows, err := s.pool.Query(ctx, agencyFeedSQL, slug, MaxFeedItems)
	if err != nil {
		return AgencyFeed{}, fmt.Errorf("query agency feed: %w", err)
	}
	defer rows.Close()

	var (
		feed  AgencyFeed
		found bool
	)
	for rows.Next() {
		var (
			a          = &feed.Agency
			eventID    *uuid.UUID
			eventType  *string
			occurredAt *time.Time
			meetingID  *uuid.UUID
			title      *string
			startsAt   *time.Time
			version    *int
			item       FeedItem
		)
		if err := rows.Scan(&a.ID, &a.Slug, &a.Name, &a.Website, &a.CreatedAt,
			&eventID, &eventType, &occurredAt, &meetingID, &title, &startsAt,
			&version, &item.AgendaSummary, &item.AgendaChange); err != nil {
			return AgencyFeed{}, fmt.Errorf("scan agency feed: %w", err)
		}
		found = true
		if eventID == nil {
			continue
		}
		item.EventID = *eventID
		item.Type = EventType(*eventType)
		item.OccurredAt = *occurredAt
		item.MeetingID = *meetingID
		item.MeetingTitle = *title
		item.MeetingStartsAt = *startsAt
		item.AgendaVersion = version
		feed.Items = append(feed.Items, item)
	}
	if err := rows.Err(); err != nil {
		return AgencyFeed{}, fmt.Errorf("read agency feed: %w", err)
	}
	if !found {
		return AgencyFeed{}, fmt.Errorf("agency %q: %w", slug, ErrNotFound)
	}
	return feed, nil
}
