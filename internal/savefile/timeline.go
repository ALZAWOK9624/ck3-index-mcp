package savefile

import (
	"io"
	"sort"
	"strconv"
	"strings"
)

// A save records a position, not a chronicle. Seven of its blocks are the
// exception, and they are the only ones: every other block was checked and
// carries no date at all. A title keeps the succession that produced its
// holder; characters keep what they remember; a scheme, a court appointment
// and a vassal contract each keep when they began; two houses keep every
// change in their standing, with the save's own written reason; and a
// struggle keeps its opening and each catalyst that moved it.
//
// Blocks deliberately left out, because they date nothing and inventing a
// date for them would be worse than omitting them: secrets, relations,
// opinions, council tasks, raids and armies. They are standing facts, and a
// caller who wants them reads them through the document walk.
//
// Nothing here infers. A title changing hands on a date is what the save
// records; why it changed is the caller's reading of the type it carries.

const (
	blockCharacterMemory = "character_memory_manager"
	blockSchemes         = "schemes"
	blockCourtPositions  = "court_positions"
	blockVassalContracts = "vassal_contracts"
	blockHouseRelations  = "house_relations"
	blockStruggleManager = "struggle_manager"
	blockDatabase        = "database"
	blockActive          = "active"
	fieldHistory         = "history"
	fieldType            = "type"
	fieldParticipants    = "participants"
	fieldCreationDate    = "creation_date"
	fieldEndDate         = "end_date"
	fieldMemories        = "memories"
	fieldStatus          = "status"
	fieldOwner           = "owner"
	fieldTarget          = "target"
	fieldProgress        = "progress"
	fieldExposed         = "scheme_exposed"
	fieldCourtPosition   = "court_position"
	fieldEmployee        = "employee"
	fieldHireDate        = "hire_date"
	fieldContractGroup   = "contract_group"
	fieldLiege           = "liege"
	fieldVassal          = "vassal"
	fieldHouses          = "houses"
	fieldLevel           = "level"
	fieldAmount          = "amount"
	fieldChangeReason    = "change_reason"
	fieldStruggleType    = "struggle_type"
	fieldStruggleStart   = "struggle_start_date"
	fieldStruggleData    = "struggle_progress_data"
	fieldStrugglePhase   = "struggle_phase"
	fieldCatalysts       = "catalysts"
	fieldTime            = "time"
	// The save spells several different facts `date`: a title's held-since,
	// a scheme's start, a contract's signing. The constant is named for the
	// meaning, not the key.
	fieldStartedOn = "date"
	// A dead character keeps their memories under dead_data rather than
	// alive_data, and the two are otherwise the same shape.
	fieldDeadData = "dead_data"
)

// noCharacter is the sentinel CK3 writes where a character reference is
// absent. It is uint32's maximum, and reporting it as a save id would invent
// a character that does not exist.
const noCharacter int64 = 4294967295

// Timeline event kinds.
const (
	EventTitleHistory    = "title_history"
	EventCharacterMemory = "character_memory"
	// EventScheme is a scheme's opening. Unlike the other two it is also a
	// present tense: the scheme is still running, and its status says so.
	EventScheme = "scheme"
	// EventCourtPosition is one appointment to a court position.
	EventCourtPosition = "court_position"
	// EventVassalContract is one feudal contract's signing.
	EventVassalContract = "vassal_contract"
	// EventHouseRelation is one change in standing between two houses. It is
	// the only source that carries the save's own written reason.
	EventHouseRelation = "house_relation"
	// EventStruggle is a struggle's opening, or one catalyst that moved it.
	EventStruggle = "struggle"
)

// TimelineKinds is every event kind a timeline can collect.
var TimelineKinds = []string{
	EventTitleHistory, EventCharacterMemory, EventScheme,
	EventCourtPosition, EventVassalContract, EventHouseRelation, EventStruggle,
}

// TimelineEvent is one dated thing the save records as having happened.
type TimelineEvent struct {
	Date string `json:"date"`
	// Kind is which block the event came from.
	Kind string `json:"kind"`
	// Type is the save's own label: a succession type such as created or
	// conquest, or a memory type such as ascended_throne_memory.
	Type string `json:"type,omitempty"`

	// Title and TitleID identify the title a succession belongs to.
	Title   string `json:"title,omitempty"`
	TitleID int64  `json:"title_id,omitempty"`
	// Holder is who took the title on this date.
	Holder int64 `json:"holder,omitempty"`

	// MemoryID is the memory's key in the database.
	MemoryID int64 `json:"memory_id,omitempty"`
	// Participants maps the memory's role names to character save ids.
	Participants map[string]int64 `json:"participants,omitempty"`
	// EndDate is when a memory stops being remembered.
	EndDate string `json:"end_date,omitempty"`

	// Owner is who set a scheme in motion, and Target who it is aimed at.
	Owner      int64  `json:"owner,omitempty"`
	Target     int64  `json:"target,omitempty"`
	TargetType string `json:"target_type,omitempty"`
	// Status, Progress and Exposed are a running scheme's current standing.
	Status   string `json:"status,omitempty"`
	Progress int64  `json:"progress,omitempty"`
	Exposed  bool   `json:"exposed,omitempty"`

	// Houses are the two dynasty houses a relation change is between.
	Houses []int64 `json:"houses,omitempty"`
	// Level is the standing a house relation moved to.
	Level string `json:"level,omitempty"`
	// Amount is how far a house relation moved.
	Amount float64 `json:"amount,omitempty"`
	// Reason is the save's own written explanation, kept verbatim.
	//
	// It is already localized display text and carries CK3's inline
	// formatting codes, including the character ids behind its links. Those
	// are stripped nowhere: they identify who the sentence is about, and a
	// cleaner string would lose them.
	Reason string `json:"reason,omitempty"`
}

// TimelineQuery narrows one pass.
type TimelineQuery struct {
	// Character keeps only events that name this character: a title's new
	// holder, a memory participant or owner, either end of a scheme, an
	// employer or employee, a liege or vassal.
	Character int64
	// Kinds restricts collection to these event kinds. Empty collects every
	// kind, and a block no kind asks for is skipped rather than parsed.
	Kinds []string
	// MaxEvents caps what is returned, after sorting.
	MaxEvents int
}

// wants reports whether one event kind is being collected.
func (q TimelineQuery) wants(kind string) bool {
	if len(q.Kinds) == 0 {
		return true
	}
	for _, requested := range q.Kinds {
		if requested == kind {
			return true
		}
	}
	return false
}

// maxCollectedEvents bounds what one pass holds before sorting.
//
// The window cannot be applied while streaming: the blocks arrive in file
// order, so cutting at MaxEvents during the walk would return the first N
// title successions and never reach the memories or schemes at all. Events
// are collected, sorted, and only then cut — within this ceiling, which at
// roughly a hundred bytes each is a few megabytes at worst.
const maxCollectedEvents = 50000

// TimelineScan is what one pass collected.
type TimelineScan struct {
	Events []TimelineEvent
	// Total counts every matching event, including those past MaxEvents.
	Total int
	// Totals counts matching events by kind, so a source with nothing in the
	// returned window is still visibly present.
	Totals map[string]int
	// The Seen counters report how much of each block was examined, so an
	// empty answer can be told apart from a block that was not there.
	TitlesSeen         int
	MemoriesSeen       int
	SchemesSeen        int
	CourtPositionsSeen int
	ContractsSeen      int
	HouseRelationsSeen int
	StrugglesSeen      int
	Truncated          []string
	BytesRead          int64
}

// Examined reports what each block yielded, keyed by the event kind it feeds.
func (s *TimelineScan) Examined() map[string]int {
	return map[string]int{
		EventTitleHistory:    s.TitlesSeen,
		EventCharacterMemory: s.MemoriesSeen,
		EventScheme:          s.SchemesSeen,
		EventCourtPosition:   s.CourtPositionsSeen,
		EventVassalContract:  s.ContractsSeen,
		EventHouseRelation:   s.HouseRelationsSeen,
		EventStruggle:        s.StrugglesSeen,
	}
}

// ScanTimeline makes one bounded streaming pass and collects dated events.
//
// The blocks are read in the order the save writes them — landed_titles, then
// living, then the memory database — so a character's own memory ids are
// known by the time the memories are reached. Matching also accepts a
// participant directly, so the result does not depend on that order holding.
func ScanTimeline(encoding Encoding, src io.Reader, resolver *TokenMap,
	query TimelineQuery, limits Limits) (*TimelineScan, error) {
	if resolver == nil && encoding != EncodingText {
		return nil, newError(ErrTokenMap, "a token map is required to navigate a binary gamestate")
	}
	if query.MaxEvents <= 0 {
		query.MaxEvents = 200
	}
	streamLimits := limits
	streamLimits.MaxTokens = limits.gamestateCeiling()/2 + 1

	scan := &TimelineScan{}
	context := &characterContext{memories: map[int64]struct{}{}}
	decoder := NewStreamDecoderFor(encoding, src, streamLimits)
	err := readObjectStream(decoder, resolver, func(name string, value Token, d *StreamDecoder) error {
		switch name {
		case blockLandedTitles:
			if !query.wants(EventTitleHistory) {
				return d.SkipValue(value)
			}
			return scan.readTitleHistories(d, resolver, value, query, limits)
		case blockLiving, blockDead, blockCharacters:
			// Reached only to learn what one character holds: the memories
			// they remember, and the house whose relations are theirs.
			if query.Character == 0 ||
				(!query.wants(EventCharacterMemory) && !query.wants(EventHouseRelation)) {
				return d.SkipValue(value)
			}
			return scan.readCharacterContext(d, resolver, value, query, limits, context)
		case blockCharacterMemory:
			if !query.wants(EventCharacterMemory) {
				return d.SkipValue(value)
			}
			return scan.readMemories(d, resolver, value, query, limits, context.memories)
		case blockSchemes:
			if !query.wants(EventScheme) {
				return d.SkipValue(value)
			}
			return scan.readSchemes(d, resolver, value, query, limits)
		case blockCourtPositions:
			if !query.wants(EventCourtPosition) {
				return d.SkipValue(value)
			}
			return scan.readCourtPositions(d, resolver, value, query, limits)
		case blockVassalContracts:
			if !query.wants(EventVassalContract) {
				return d.SkipValue(value)
			}
			return scan.readVassalContracts(d, resolver, value, query, limits)
		case blockHouseRelations:
			if !query.wants(EventHouseRelation) {
				return d.SkipValue(value)
			}
			return scan.readHouseRelations(d, resolver, value, query, limits, context)
		case blockStruggleManager:
			if !query.wants(EventStruggle) {
				return d.SkipValue(value)
			}
			return scan.readStruggles(d, resolver, value, query, limits)
		default:
			return d.SkipValue(value)
		}
	})
	if err != nil {
		return nil, err
	}
	sortTimeline(scan.Events)
	scan.window(query.MaxEvents)
	scan.BytesRead = decoder.Consumed()
	return scan, nil
}

// keep records one matching event.
//
// Totals keep counting past the collection ceiling, so a caller can tell
// "these are all of them" from "these are 50 of 2222".
func (s *TimelineScan) keep(event TimelineEvent, query TimelineQuery) {
	s.Total++
	if s.Totals == nil {
		s.Totals = map[string]int{}
	}
	s.Totals[event.Kind]++
	if len(s.Events) >= maxCollectedEvents {
		s.Truncated = appendTruncated(s.Truncated, !hasTruncation(s.Truncated, "collection_ceiling"), "collection_ceiling")
		return
	}
	s.Events = append(s.Events, event)
}

func hasTruncation(reasons []string, reason string) bool {
	for _, seen := range reasons {
		if seen == reason {
			return true
		}
	}
	return false
}

// window cuts the sorted events down to what was asked for.
//
// The most recent are kept rather than the first, because a save's oldest
// entries are its world-generation boilerplate and its newest are what the
// caller means by "what happened". They stay in chronological order.
func (s *TimelineScan) window(max int) {
	if max <= 0 || len(s.Events) <= max {
		return
	}
	s.Events = s.Events[len(s.Events)-max:]
	s.Truncated = appendTruncated(s.Truncated, !hasTruncation(s.Truncated, "events"), "events")
}

// readTitleHistories collects every dated succession entry a title carries.
func (s *TimelineScan) readTitleHistories(d *StreamDecoder, resolver *TokenMap,
	value Token, query TimelineQuery, limits Limits) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	return readObjectStream(d, resolver, func(name string, inner Token, d *StreamDecoder) error {
		// The outer landed_titles wraps an inner map of the same name.
		if name != blockLandedTitles || inner.Kind != KindOpen {
			return d.SkipValue(inner)
		}
		return readNumberedEntries(d, resolver, limits, func(id int64, entry Token, d *StreamDecoder) error {
			s.TitlesSeen++
			key := ""
			var events []TimelineEvent
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldKey:
					key = string(fieldValue.Text)
				case fieldHistory:
					collected, err := readTitleHistory(d, resolver, fieldValue, limits)
					events = collected
					return err
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			for _, event := range events {
				if query.Character != 0 && event.Holder != query.Character {
					continue
				}
				event.Title, event.TitleID = key, id
				s.keep(event, query)
			}
			return nil
		})
	})
}

// readTitleHistory reads the `history={ <date>={ type= holder= } }` block.
//
// The entry keys are dates rather than numbers, so this cannot go through
// readNumberedEntries: a date is the fact being read, not an index.
func readTitleHistory(d *StreamDecoder, resolver *TokenMap, value Token, limits Limits) ([]TimelineEvent, error) {
	if value.Kind != KindOpen {
		return nil, d.SkipValue(value)
	}
	var events []TimelineEvent
	err := readObjectStream(d, resolver, func(when string, entry Token, d *StreamDecoder) error {
		if len(events) >= limits.MaxArrayItems {
			return d.SkipValue(entry)
		}
		event := TimelineEvent{Date: when, Kind: EventTitleHistory}
		if entry.Kind != KindOpen {
			// A bare `<date>=<holder>` form still dates a succession.
			event.Holder = signedOf(entry)
			events = append(events, event)
			return nil
		}
		if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
			switch field {
			case fieldType:
				event.Type = string(fieldValue.Text)
			case fieldHolder:
				event.Holder = signedOf(fieldValue)
			}
			return d.SkipValue(fieldValue)
		}); err != nil {
			return err
		}
		events = append(events, event)
		return nil
	})
	return events, err
}

// characterContext is what a character filter needs that is not on the events
// themselves: which memories they hold, and which house is theirs.
//
// Both are read from the character tables, which the save writes before the
// managers that reference them, so one pass is enough.
type characterContext struct {
	memories map[int64]struct{}
	house    int64
}

// readCharacterContext notes what one character holds, so the managers that
// come later in the file can be filtered to them.
func (s *TimelineScan) readCharacterContext(d *StreamDecoder, resolver *TokenMap, value Token,
	query TimelineQuery, limits Limits, context *characterContext) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	return readNumberedEntries(d, resolver, limits, func(id int64, entry Token, d *StreamDecoder) error {
		if id != query.Character {
			return nil
		}
		return readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
			if field == fieldDynastyHouse {
				context.house = signedOf(fieldValue)
				return d.SkipValue(fieldValue)
			}
			if field != fieldAliveData && field != fieldDeadData {
				return d.SkipValue(fieldValue)
			}
			return readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
				if sub != fieldMemories {
					return d.SkipValue(subValue)
				}
				ids, _, err := readStreamNumbers(d, subValue, limits)
				for _, memory := range ids {
					context.memories[memory] = struct{}{}
				}
				return err
			})
		})
	})
}

// readMemories collects the dated events characters remember.
func (s *TimelineScan) readMemories(d *StreamDecoder, resolver *TokenMap, value Token,
	query TimelineQuery, limits Limits, owned map[int64]struct{}) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	return readObjectStream(d, resolver, func(name string, inner Token, d *StreamDecoder) error {
		if name != blockDatabase || inner.Kind != KindOpen {
			return d.SkipValue(inner)
		}
		return readNumberedEntries(d, resolver, limits, func(id int64, entry Token, d *StreamDecoder) error {
			s.MemoriesSeen++
			event := TimelineEvent{Kind: EventCharacterMemory, MemoryID: id}
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldType:
					event.Type = string(fieldValue.Text)
				case fieldCreationDate:
					event.Date = date(fieldValue)
				case fieldEndDate:
					event.EndDate = date(fieldValue)
				case fieldParticipants:
					if fieldValue.Kind != KindOpen {
						return d.SkipValue(fieldValue)
					}
					return readObjectStream(d, resolver, func(role string, who Token, d *StreamDecoder) error {
						if event.Participants == nil {
							event.Participants = map[string]int64{}
						}
						if len(event.Participants) < limits.MaxArrayItems {
							event.Participants[role] = signedOf(who)
						}
						return d.SkipValue(who)
					})
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			if query.Character != 0 && !memoryInvolves(event, query.Character, owned) {
				return nil
			}
			s.keep(event, query)
			return nil
		})
	})
}

// readSchemes collects the schemes currently in motion.
//
// A scheme is the one source here that is both past and present: its date is
// when it was set in motion, which is an event, while its status and progress
// describe something still unresolved. Both are reported, and neither is
// turned into a prediction of how it ends.
func (s *TimelineScan) readSchemes(d *StreamDecoder, resolver *TokenMap, value Token,
	query TimelineQuery, limits Limits) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	return readObjectStream(d, resolver, func(name string, inner Token, d *StreamDecoder) error {
		if name != blockActive || inner.Kind != KindOpen {
			return d.SkipValue(inner)
		}
		return readNumberedEntries(d, resolver, limits, func(id int64, entry Token, d *StreamDecoder) error {
			s.SchemesSeen++
			event := TimelineEvent{Kind: EventScheme}
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldType:
					event.Type = string(fieldValue.Text)
				case fieldStatus:
					event.Status = string(fieldValue.Text)
				case fieldOwner:
					event.Owner = signedOf(fieldValue)
				case fieldProgress:
					event.Progress = signedOf(fieldValue)
				case fieldExposed:
					event.Exposed = fieldValue.Kind == KindBool && fieldValue.Bool
				case fieldStartedOn:
					event.Date = date(fieldValue)
				case fieldTarget:
					// The target is a tagged reference: `{ type=character
					// target=1242 }`, where type names what the id points at.
					if fieldValue.Kind != KindOpen {
						event.Target = signedOf(fieldValue)
						return nil
					}
					return readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
						switch sub {
						case fieldType:
							event.TargetType = string(subValue.Text)
						case fieldTarget:
							event.Target = signedOf(subValue)
						}
						return d.SkipValue(subValue)
					})
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			if query.Character != 0 && event.Owner != query.Character && event.Target != query.Character {
				return nil
			}
			s.keep(event, query)
			return nil
		})
	})
}

// readDatabaseEntries walks the `database={ 0={...} }` shape four of these
// blocks share, so each reader only has to describe its own fields.
func readDatabaseEntries(d *StreamDecoder, resolver *TokenMap, value Token, limits Limits,
	visit numberedVisitor) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	return readObjectStream(d, resolver, func(name string, inner Token, d *StreamDecoder) error {
		if name != blockDatabase || inner.Kind != KindOpen {
			return d.SkipValue(inner)
		}
		return readNumberedEntries(d, resolver, limits, visit)
	})
}

// readContainerList walks a list whose elements are anonymous containers,
// which is the shape a house relation's history and a struggle phase's
// history both use. readObjectStream cannot: those elements have no key.
func readContainerList(d *StreamDecoder, value Token, limits Limits,
	visit func(d *StreamDecoder) error) error {
	if value.Kind != KindOpen {
		return d.SkipValue(value)
	}
	seen := 0
	target := d.Depth() - 1
	for d.Depth() > target {
		token, err := d.Next()
		if err != nil {
			return err
		}
		if token.Kind == KindClose {
			continue
		}
		if token.Kind != KindOpen {
			continue
		}
		if seen >= limits.MaxArrayItems {
			if err := d.SkipValue(token); err != nil {
				return err
			}
			continue
		}
		seen++
		entryDepth := d.Depth()
		if err := visit(d); err != nil {
			return err
		}
		// Whatever the visitor left unread belongs to this element.
		if err := d.SkipToDepth(entryDepth - 1); err != nil {
			return err
		}
	}
	return nil
}

// readCourtPositions collects who was appointed to whose court, and when.
func (s *TimelineScan) readCourtPositions(d *StreamDecoder, resolver *TokenMap, value Token,
	query TimelineQuery, limits Limits) error {
	return readDatabaseEntries(d, resolver, value, limits,
		func(id int64, entry Token, d *StreamDecoder) error {
			s.CourtPositionsSeen++
			event := TimelineEvent{Kind: EventCourtPosition}
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldCourtPosition:
					event.Type = string(fieldValue.Text)
				case fieldEmployer:
					event.Owner = characterRef(fieldValue)
				case fieldEmployee:
					event.Target = characterRef(fieldValue)
				case fieldHireDate:
					event.Date = date(fieldValue)
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			if query.Character != 0 && event.Owner != query.Character && event.Target != query.Character {
				return nil
			}
			s.keep(event, query)
			return nil
		})
}

// readVassalContracts collects when each feudal contract was struck.
func (s *TimelineScan) readVassalContracts(d *StreamDecoder, resolver *TokenMap, value Token,
	query TimelineQuery, limits Limits) error {
	return readDatabaseEntries(d, resolver, value, limits,
		func(id int64, entry Token, d *StreamDecoder) error {
			s.ContractsSeen++
			event := TimelineEvent{Kind: EventVassalContract}
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldContractGroup:
					event.Type = string(fieldValue.Text)
				case fieldLiege:
					event.Owner = characterRef(fieldValue)
				case fieldVassal:
					event.Target = characterRef(fieldValue)
				case fieldStartedOn:
					event.Date = date(fieldValue)
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			if query.Character != 0 && event.Owner != query.Character && event.Target != query.Character {
				return nil
			}
			s.keep(event, query)
			return nil
		})
}

// readHouseRelations collects every dated change in standing between houses.
//
// This is the only source that carries the save's own written explanation:
// change_reason is finished display text naming the characters involved.
func (s *TimelineScan) readHouseRelations(d *StreamDecoder, resolver *TokenMap, value Token,
	query TimelineQuery, limits Limits, context *characterContext) error {
	return readDatabaseEntries(d, resolver, value, limits,
		func(id int64, entry Token, d *StreamDecoder) error {
			s.HouseRelationsSeen++
			var houses []int64
			var changes []TimelineEvent
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldHouses:
					values, _, err := readStreamNumbers(d, fieldValue, limits)
					houses = values
					return err
				case fieldHistory:
					return readContainerList(d, fieldValue, limits, func(d *StreamDecoder) error {
						change := TimelineEvent{Kind: EventHouseRelation}
						if err := readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
							switch sub {
							case fieldStartedOn:
								change.Date = date(subValue)
							case fieldAmount:
								change.Amount = floatOf(subValue)
							case fieldLevel:
								change.Level = string(subValue.Text)
							case fieldChangeReason:
								change.Reason = string(subValue.Text)
							}
							return d.SkipValue(subValue)
						}); err != nil {
							return err
						}
						changes = append(changes, change)
						return nil
					})
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			// A relation is between houses, so a character reaches it only
			// through their own house. Without that, a character's timeline
			// would carry every house feud in the world.
			if query.Character != 0 && !containsID(houses, context.house) {
				return nil
			}
			// The houses are named beside the history, not inside it, so they
			// can only be attached once the whole entry has been read.
			for _, change := range changes {
				change.Houses = houses
				s.keep(change, query)
			}
			return nil
		})
}

func containsID(values []int64, want int64) bool {
	if want == 0 {
		return false
	}
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// readStruggles collects a struggle's opening and every catalyst that moved
// it, both of which the save dates.
func (s *TimelineScan) readStruggles(d *StreamDecoder, resolver *TokenMap, value Token,
	query TimelineQuery, limits Limits) error {
	return readDatabaseEntries(d, resolver, value, limits,
		func(id int64, entry Token, d *StreamDecoder) error {
			s.StrugglesSeen++
			opening := TimelineEvent{Kind: EventStruggle, Status: "began"}
			var catalysts []TimelineEvent
			if err := readObjectStream(d, resolver, func(field string, fieldValue Token, d *StreamDecoder) error {
				switch field {
				case fieldStruggleType:
					opening.Type = string(fieldValue.Text)
				case fieldStruggleStart:
					opening.Date = date(fieldValue)
				case fieldStruggleData:
					if fieldValue.Kind != KindOpen {
						return d.SkipValue(fieldValue)
					}
					// One entry per phase, each with its own history.
					return readObjectStream(d, resolver, func(phase string, phaseValue Token, d *StreamDecoder) error {
						if phaseValue.Kind != KindOpen {
							return d.SkipValue(phaseValue)
						}
						return readObjectStream(d, resolver, func(sub string, subValue Token, d *StreamDecoder) error {
							if sub != fieldHistory {
								return d.SkipValue(subValue)
							}
							return readContainerList(d, subValue, limits, func(d *StreamDecoder) error {
								catalyst := TimelineEvent{Kind: EventStruggle, Status: phase}
								if err := readObjectStream(d, resolver, func(name string, entryValue Token, d *StreamDecoder) error {
									switch name {
									case fieldCatalysts:
										catalyst.Type = string(entryValue.Text)
									case fieldStrugglePhase:
										catalyst.Status = string(entryValue.Text)
									case fieldTime:
										catalyst.Date = date(entryValue)
									case fieldCharacter:
										catalyst.Owner = characterRef(entryValue)
									}
									return d.SkipValue(entryValue)
								}); err != nil {
									return err
								}
								catalysts = append(catalysts, catalyst)
								return nil
							})
						})
					})
				}
				return d.SkipValue(fieldValue)
			}); err != nil {
				return err
			}
			// A struggle is world-scale. Under a character filter only the
			// catalysts that name that character survive, because a request
			// for one person's timeline is not a request for world history.
			if opening.Date != "" && query.Character == 0 {
				s.keep(opening, query)
			}
			for _, catalyst := range catalysts {
				if query.Character != 0 && catalyst.Owner != query.Character {
					continue
				}
				s.keep(catalyst, query)
			}
			return nil
		})
}

// characterRef reads a character reference, treating CK3's absent sentinel as
// absent rather than as save id 4294967295.
func characterRef(token Token) int64 {
	if value := signedOf(token); value != noCharacter {
		return value
	}
	return 0
}

func memoryInvolves(event TimelineEvent, character int64, owned map[int64]struct{}) bool {
	if _, remembered := owned[event.MemoryID]; remembered {
		return true
	}
	for _, who := range event.Participants {
		if who == character {
			return true
		}
	}
	return false
}

// sortTimeline orders events oldest first.
//
// Dates are compared by component rather than as text, because a save writes
// them unpadded and "1066.9.1" sorts after "1066.10.1" as a string.
func sortTimeline(events []TimelineEvent) {
	sort.SliceStable(events, func(a, b int) bool {
		return compareSaveDates(events[a].Date, events[b].Date) < 0
	})
}

func compareSaveDates(left, right string) int {
	leftParts, rightParts := strings.Split(left, "."), strings.Split(right, ".")
	for index := 0; index < 4; index++ {
		a, b := saveDateComponent(leftParts, index), saveDateComponent(rightParts, index)
		if a != b {
			if a < b {
				return -1
			}
			return 1
		}
	}
	return 0
}

func saveDateComponent(parts []string, index int) int {
	if index >= len(parts) {
		return 0
	}
	value, err := strconv.Atoi(parts[index])
	if err != nil {
		return 0
	}
	return value
}
