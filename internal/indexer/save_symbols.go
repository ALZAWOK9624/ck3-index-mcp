package indexer

import "context"

// SaveSymbolCheck answers "is this id still defined?" for ids that came from
// outside the index — in practice, out of a save file.
//
// The index already answers this during a scan, but only for references it
// parsed out of scripts, and only one transaction at a time. A save carries
// thousands of ids and none of them are in `refs`, so the existing per-id
// query would mean thousands of round trips. This loads the active symbol set
// once and answers from memory afterwards, which is the same shape the scanner
// itself uses.
type SaveSymbolCheck struct {
	names map[string]bool
}

// ActiveSymbols loads every object name the current index considers active.
//
// "Active" means defined in a file that is not overridden by a
// higher-precedence source, which is exactly the set the game would see.
func (db *DB) ActiveSymbols(ctx context.Context) (*SaveSymbolCheck, error) {
	names := map[string]bool{}
	rows, err := db.sql.QueryContext(ctx, `SELECT DISTINCT o.object_type, o.name
		FROM objects o JOIN files f ON f.id=o.file_id
		WHERE f.overridden=0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			return nil, err
		}
		// Both keys are stored so a caller can ask with or without a type,
		// matching how the scanner resolves references.
		names[typ+":"+name] = true
		names[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &SaveSymbolCheck{names: names}, nil
}

// Defined reports whether an id of the given object type is defined.
//
// It mirrors the scanner's own resolution rule: a typed match wins, and an
// untyped match still counts, because CK3 ids are unique across most types and
// the index records both forms.
func (c *SaveSymbolCheck) Defined(objectType, name string) bool {
	if c == nil || name == "" {
		return false
	}
	if objectType != "" && c.names[objectType+":"+name] {
		return true
	}
	return c.names[name]
}

// Size reports how many distinct symbols were loaded.
func (c *SaveSymbolCheck) Size() int {
	if c == nil {
		return 0
	}
	return len(c.names)
}
