package indexer

var objectRefKinds = map[string]bool{
	"trait": true, "modifier": true, "nickname": true, "event": true, "on_action": true,
	"faith": true, "religion": true, "culture": true, "title": true, "character": true,
	"culture_pillar": true, "culture_tradition": true, "name_list": true, "innovation": true,
	"government": true, "law": true, "secret": true, "casus_belli_type": true,
	"men_at_arms_type": true, "building": true, "artifact": true, "holy_site": true,
}

func isObjectRefKind(kind string) bool {
	return objectRefKinds[kind]
}
