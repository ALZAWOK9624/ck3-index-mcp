package indexer

var objectRefKinds = map[string]bool{
	"trait": true, "modifier": true, "nickname": true, "event": true, "on_action": true,
	"faith": true, "religion": true, "culture": true, "title": true, "character": true,
	"dynasty": true, "dynasty_house": true,
	"culture_pillar": true, "culture_tradition": true, "name_list": true, "innovation": true,
	"government": true, "law": true, "law_group": true, "secret": true, "casus_belli_type": true,
	"doctrine": true, "doctrine_group": true,
	"game_rule": true, "game_rule_setting": true,
	"focus": true, "lifestyle": true, "lifestyle_perk": true,
	"achievement": true, "achievement_group": true,
	"scheme_type": true, "scheme_agent_type": true, "scheme_pulse_action": true, "scheme_countermeasure": true,
	"activity_group_type": true, "activity_intent": true, "activity_guest_invite_rule": true, "activity_pulse_action": true,
	"artifact_type": true, "artifact_slot": true, "artifact_feature": true, "artifact_visual": true,
	"bookmark_group": true, "subject_contract": true, "subject_contract_group": true,
	"tax_slot": true, "tax_obligation": true, "situation_group_type": true, "legend": true, "legend_seed": true,
	"court_amenity_category": true, "court_amenity_level": true,
	"scripted_variable": true,
	"death_reason":      true,
	"religion_family":   true,
	"fervor_modifier":   true,
	"men_at_arms_type":  true, "building": true, "artifact": true, "holy_site": true,
}

var schemeTypeReferenceContext = map[string]bool{
	"can_start_scheme":    true,
	"start_scheme":        true,
	"random_scheme":       true,
	"is_scheming_against": true,
	"add_scheme_cooldown": true,
}

func isObjectRefKind(kind string) bool {
	return objectRefKinds[kind]
}
