package indexer

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	// EventTopologyHTMLSchemaVersion identifies the self-contained event-chain
	// renderer contract. It is deliberately separate from GUI preview HTML: an
	// event-chain graph is evidence navigation, not a Jomini runtime preview.
	EventTopologyHTMLSchemaVersion = "ck3-event-topology-html/v1"
	EventTopologyHTMLMaxBytes      = 1 << 20
	// EventTopologyHTMLMaxInputBytes bounds raw topology string data before
	// JSON encoding. It keeps a malformed or directly-called renderer input
	// from allocating a large JSON payload and then a second large document.
	EventTopologyHTMLMaxInputBytes = 128 << 10

	eventTopologyHTMLMaxInputItems = 2048
)

// EventTopologyHTMLPreview is a bounded, standalone browser view over an
// already-filtered LLMTopology. The renderer never queries the database or
// reparses script, so callers must pass the same visibility-redacted topology
// they return in their structured result.
type EventTopologyHTMLPreview struct {
	SchemaVersion           string `json:"schema_version"`
	Document                string `json:"document,omitempty"`
	Bytes                   int    `json:"bytes"`
	SHA256                  string `json:"sha256"`
	NodeCount               int    `json:"node_count"`
	EdgeCount               int    `json:"edge_count"`
	ExternalEndpointCount   int    `json:"external_endpoint_count"`
	UnresolvedEndpointCount int    `json:"unresolved_endpoint_count"`
	DynamicEndpointCount    int    `json:"dynamic_endpoint_count"`
	BoundedEndpointCount    int    `json:"bounded_endpoint_count"`
	Scripts                 bool   `json:"scripts"`
	ScriptPolicy            string `json:"script_policy"`
	ScriptSHA256            string `json:"script_sha256,omitempty"`
	ExternalRequests        bool   `json:"external_requests"`
	ModelReadable           bool   `json:"model_readable"`
}

// RenderEventTopologyHTML renders a fixed-script, no-network inspector for an
// event/on_action topology. Missing endpoints are represented only inside the
// document as derived dashed stubs; they are not added to the topology payload
// and are never presented as indexed definitions.
func RenderEventTopologyHTML(topology LLMTopology) (EventTopologyHTMLPreview, error) {
	payloadUpperBound, err := eventTopologyHTMLPayloadUpperBound(topology)
	if err != nil {
		return EventTopologyHTMLPreview{}, err
	}
	payload, err := json.Marshal(topology)
	if err != nil {
		return EventTopologyHTMLPreview{}, fmt.Errorf("marshal event topology HTML payload: %w", err)
	}
	if len(payload) > payloadUpperBound {
		return EventTopologyHTMLPreview{}, fmt.Errorf("event topology HTML payload exceeded its preflight bound")
	}
	if len(payload) > eventTopologyHTMLMaxPayloadBytes {
		return EventTopologyHTMLPreview{}, fmt.Errorf("event topology HTML inspector payload exceeds %d bytes", eventTopologyHTMLMaxPayloadBytes)
	}

	scriptDigest := sha256.Sum256([]byte(eventTopologyHTMLInspectorScript))
	scriptPolicy := "sha256-" + base64.StdEncoding.EncodeToString(scriptDigest[:])
	endpointCounts := eventTopologyExternalEndpointCounts(topology)

	var output strings.Builder
	output.Grow(eventTopologyHTMLStaticDocumentBytes + len(payload))
	output.WriteString("<!doctype html>\n<html lang=\"und\">\n<head>\n<meta charset=\"utf-8\">\n")
	output.WriteString("<meta http-equiv=\"Content-Security-Policy\" content=\"default-src 'none'; style-src 'unsafe-inline'; script-src '")
	output.WriteString(scriptPolicy)
	output.WriteString("'; img-src 'none'; font-src 'none'; media-src 'none'; object-src 'none'; connect-src 'none'; base-uri 'none'; form-action 'none'\">\n")
	output.WriteString("<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">\n<title>CK3 event-chain topology inspector</title>\n<style>\n")
	output.WriteString(eventTopologyHTMLInspectorStyles)
	output.WriteString(eventTopologyHTMLInspectorEndpointStyles)
	output.WriteString("</style>\n</head>\n<body>\n<main id=\"ck3-event-topology-inspector\" data-ck3-schema=\"")
	output.WriteString(EventTopologyHTMLSchemaVersion)
	output.WriteString("\">\n<header class=\"ck3-topology-toolbar\">\n<div class=\"ck3-topology-identity\"><strong>Event / on_action chain</strong><span id=\"ck3-topology-meta\"></span></div>\n")
	output.WriteString("<label class=\"ck3-topology-field ck3-topology-search\"><span>Search</span><input id=\"ck3-topology-search\" type=\"search\" autocomplete=\"off\" placeholder=\"id, title, source\"></label>\n")
	output.WriteString("<label class=\"ck3-topology-field\"><span>Relation</span><select id=\"ck3-topology-relation\"></select></label>\n")
	output.WriteString("<label class=\"ck3-topology-field\"><span>Phase</span><select id=\"ck3-topology-phase\"></select></label>\n")
	output.WriteString("<label class=\"ck3-topology-field\"><span>Resolution</span><select id=\"ck3-topology-resolution\"></select></label>\n")
	output.WriteString("<button id=\"ck3-topology-fit\" type=\"button\">Fit</button><button id=\"ck3-topology-zoom-in\" type=\"button\" aria-label=\"Zoom in\">+</button><button id=\"ck3-topology-zoom-out\" type=\"button\" aria-label=\"Zoom out\">-</button>\n</header>\n")
	output.WriteString("<section class=\"ck3-topology-workspace\">\n<section class=\"ck3-topology-canvas-wrap\" aria-label=\"Event chain graph. Drag to pan; use the mouse wheel or zoom controls to zoom.\"><svg id=\"ck3-topology-canvas\" role=\"img\" aria-label=\"Event and on_action dependency graph\"><g id=\"ck3-topology-scene\"></g></svg></section>\n")
	output.WriteString("<aside id=\"ck3-topology-details\" class=\"ck3-topology-details\" aria-live=\"polite\"></aside>\n</section>\n<footer id=\"ck3-topology-status\" class=\"ck3-topology-status\"></footer>\n")
	// json.Marshal escapes '<', '>', and '&', so topology strings cannot close
	// this inert JSON script element or become markup. The executable script is
	// a separate fixed literal protected by its CSP hash.
	output.WriteString("<script id=\"ck3-event-topology-data\" type=\"application/json\">")
	output.Write(payload)
	output.WriteString("</script>\n<script>")
	output.WriteString(eventTopologyHTMLInspectorScript)
	output.WriteString("</script>\n</main>\n</body>\n</html>\n")

	document := output.String()
	if len(document) > EventTopologyHTMLMaxBytes {
		return EventTopologyHTMLPreview{}, fmt.Errorf("event topology HTML inspector exceeds %d bytes", EventTopologyHTMLMaxBytes)
	}
	digest := sha256.Sum256([]byte(document))
	return EventTopologyHTMLPreview{
		SchemaVersion:           EventTopologyHTMLSchemaVersion,
		Document:                document,
		Bytes:                   len(document),
		SHA256:                  fmt.Sprintf("%x", digest),
		NodeCount:               len(topology.Nodes),
		EdgeCount:               len(topology.Edges),
		ExternalEndpointCount:   endpointCounts.External,
		UnresolvedEndpointCount: endpointCounts.Unresolved,
		DynamicEndpointCount:    endpointCounts.Dynamic,
		BoundedEndpointCount:    endpointCounts.Bounded,
		Scripts:                 true,
		ScriptPolicy:            "fixed-generator-script",
		ScriptSHA256:            scriptPolicy,
		ExternalRequests:        false,
		ModelReadable:           true,
	}, nil
}

const (
	eventTopologyHTMLStaticDocumentBytes = len(eventTopologyHTMLInspectorStyles) + len(eventTopologyHTMLInspectorEndpointStyles) + len(eventTopologyHTMLInspectorScript) + 4096
	eventTopologyHTMLMaxPayloadBytes     = EventTopologyHTMLMaxBytes - eventTopologyHTMLStaticDocumentBytes
)

type eventTopologyExternalEndpointCountsResult struct {
	External, Unresolved, Dynamic, Bounded int
}

type eventTopologyExternalEndpointKind string

const (
	eventTopologyEndpointResolved   eventTopologyExternalEndpointKind = "resolved"
	eventTopologyEndpointBounded    eventTopologyExternalEndpointKind = "bounded"
	eventTopologyEndpointDynamic    eventTopologyExternalEndpointKind = "dynamic"
	eventTopologyEndpointUnresolved eventTopologyExternalEndpointKind = "unresolved"
	eventTopologyEndpointUnknown    eventTopologyExternalEndpointKind = "unknown"
)

func eventTopologyExternalEndpointCounts(topology LLMTopology) eventTopologyExternalEndpointCountsResult {
	known := make(map[string]bool, len(topology.Nodes))
	for _, node := range topology.Nodes {
		known[node.ID] = true
	}
	missing := map[string]eventTopologyExternalEndpointKind{}
	for _, edge := range topology.Edges {
		for _, id := range []string{edge.From, edge.To} {
			if id == "" || known[id] {
				continue
			}
			kind := classifyEventTopologyExternalEndpoint(edge.Resolution, topology.Truncated)
			if previous, exists := missing[id]; !exists || eventTopologyEndpointRank(kind) > eventTopologyEndpointRank(previous) {
				missing[id] = kind
			}
		}
	}
	counts := eventTopologyExternalEndpointCountsResult{External: len(missing)}
	for _, kind := range missing {
		switch kind {
		case eventTopologyEndpointUnresolved:
			counts.Unresolved++
		case eventTopologyEndpointDynamic:
			counts.Dynamic++
		case eventTopologyEndpointBounded:
			counts.Bounded++
		}
	}
	return counts
}

func classifyEventTopologyExternalEndpoint(resolution string, truncated bool) eventTopologyExternalEndpointKind {
	switch strings.ToLower(strings.TrimSpace(resolution)) {
	case "unresolved":
		return eventTopologyEndpointUnresolved
	case "dynamic":
		return eventTopologyEndpointDynamic
	case "resolved":
		if truncated {
			return eventTopologyEndpointBounded
		}
		return eventTopologyEndpointResolved
	default:
		return eventTopologyEndpointUnknown
	}
}

func eventTopologyEndpointRank(kind eventTopologyExternalEndpointKind) int {
	switch kind {
	case eventTopologyEndpointUnresolved:
		return 4
	case eventTopologyEndpointDynamic:
		return 3
	case eventTopologyEndpointUnknown:
		return 2
	case eventTopologyEndpointBounded:
		return 1
	default:
		return 0
	}
}

type eventTopologyHTMLInputBudget struct {
	rawStringBytes int
	payloadBytes   int
	items          int
}

// eventTopologyHTMLPayloadUpperBound validates the topology before JSON
// marshaling. JSON can expand a hostile byte to a six-byte escape, so the
// upper bound deliberately uses that worst case instead of allocating a trial
// payload merely to find out that it is too large.
func eventTopologyHTMLPayloadUpperBound(topology LLMTopology) (int, error) {
	budget := eventTopologyHTMLInputBudget{}
	if err := budget.addPayload(512); err != nil {
		return 0, err
	}
	if err := budget.addString(topology.Center); err != nil {
		return 0, err
	}
	if err := budget.addString(topology.Direction); err != nil {
		return 0, err
	}
	for _, node := range topology.Nodes {
		if err := budget.addItem(256); err != nil {
			return 0, err
		}
		for _, value := range []string{node.ID, node.Type, node.Name, node.EventType, node.Title, node.Source, node.Path} {
			if err := budget.addString(value); err != nil {
				return 0, err
			}
		}
	}
	for _, edge := range topology.Edges {
		if err := budget.addItem(288); err != nil {
			return 0, err
		}
		for _, value := range []string{edge.From, edge.To, edge.Relation, edge.Phase, edge.Confidence, edge.Resolution, edge.Reason, edge.Source, edge.Path} {
			if err := budget.addString(value); err != nil {
				return 0, err
			}
		}
	}
	for _, id := range topology.Roots {
		if err := budget.addItem(12); err != nil {
			return 0, err
		}
		if err := budget.addString(id); err != nil {
			return 0, err
		}
	}
	for _, id := range topology.Leaves {
		if err := budget.addItem(12); err != nil {
			return 0, err
		}
		if err := budget.addString(id); err != nil {
			return 0, err
		}
	}
	for _, cycle := range topology.Cycles {
		if err := budget.addItem(8); err != nil {
			return 0, err
		}
		for _, id := range cycle {
			if err := budget.addItem(12); err != nil {
				return 0, err
			}
			if err := budget.addString(id); err != nil {
				return 0, err
			}
		}
	}
	for _, path := range topology.PathsFromCenter {
		if err := budget.addItem(96); err != nil {
			return 0, err
		}
		if err := budget.addString(path.To); err != nil {
			return 0, err
		}
		for _, id := range path.Nodes {
			if err := budget.addItem(12); err != nil {
				return 0, err
			}
			if err := budget.addString(id); err != nil {
				return 0, err
			}
		}
	}
	return budget.payloadBytes, nil
}

func (budget *eventTopologyHTMLInputBudget) addItem(fixedBytes int) error {
	budget.items++
	if budget.items > eventTopologyHTMLMaxInputItems {
		return fmt.Errorf("event topology HTML input exceeds %d topology items", eventTopologyHTMLMaxInputItems)
	}
	return budget.addPayload(fixedBytes)
}

func (budget *eventTopologyHTMLInputBudget) addString(value string) error {
	if len(value) > EventTopologyHTMLMaxInputBytes-budget.rawStringBytes {
		return fmt.Errorf("event topology HTML input exceeds %d raw string bytes", EventTopologyHTMLMaxInputBytes)
	}
	budget.rawStringBytes += len(value)
	return budget.addPayload(2 + 6*len(value))
}

func (budget *eventTopologyHTMLInputBudget) addPayload(bytes int) error {
	if bytes < 0 || budget.payloadBytes > eventTopologyHTMLMaxPayloadBytes-bytes {
		return fmt.Errorf("event topology HTML input exceeds the %d-byte document budget", EventTopologyHTMLMaxBytes)
	}
	budget.payloadBytes += bytes
	return nil
}

const eventTopologyHTMLInspectorStyles = `
:root{color-scheme:dark;--bg:#10151d;--panel:#171f2b;--panel2:#202b39;--line:#38506a;--text:#e7edf5;--muted:#a9b8c8;--accent:#72b7ff;--warning:#e7af4e;--danger:#e88484;--success:#71d6a4}
*{box-sizing:border-box}html,body{margin:0;min-height:100%;background:var(--bg);color:var(--text);font:13px/1.45 "Noto Sans CJK SC","Microsoft YaHei UI","Segoe UI",sans-serif}button,input,select{font:inherit}button,select,input{border:1px solid var(--line);border-radius:5px;background:#111923;color:var(--text);min-height:28px}button{padding:2px 10px;cursor:pointer}button:hover{border-color:var(--accent);background:#17283a}input,select{padding:2px 7px;min-width:0}.ck3-topology-toolbar{display:flex;align-items:end;gap:8px;flex-wrap:wrap;padding:10px 14px;border-bottom:1px solid var(--line);background:var(--panel)}.ck3-topology-identity{display:flex;flex-direction:column;min-width:190px;margin-right:auto}.ck3-topology-identity strong{color:var(--accent);font-size:14px}.ck3-topology-identity span{color:var(--muted);font-size:11px}.ck3-topology-field{display:flex;flex-direction:column;gap:2px;color:var(--muted);font-size:11px}.ck3-topology-field span{white-space:nowrap}.ck3-topology-field select{max-width:180px}.ck3-topology-search input{width:min(260px,28vw)}.ck3-topology-workspace{display:grid;grid-template-columns:minmax(0,1fr) minmax(260px,340px);min-height:620px;height:calc(100vh - 108px)}.ck3-topology-canvas-wrap{position:relative;overflow:hidden;border-right:1px solid var(--line);background:radial-gradient(circle at 16px 16px,#263548 1px,transparent 1.5px),#0c1219;background-size:32px 32px}.ck3-topology-canvas-wrap:focus-within{outline:2px solid var(--accent);outline-offset:-2px}.ck3-topology-canvas{display:block;width:100%;height:100%;touch-action:none}.ck3-topology-details{overflow:auto;padding:14px;background:var(--panel);font-size:12px}.ck3-topology-details h2{font-size:15px;margin:0 0 6px;color:var(--accent);word-break:break-word}.ck3-topology-details h3{font-size:12px;margin:16px 0 7px;color:var(--muted);text-transform:uppercase;letter-spacing:.04em}.ck3-topology-details p{margin:5px 0;color:var(--muted)}.ck3-topology-details dl{display:grid;grid-template-columns:82px minmax(0,1fr);gap:5px 9px;margin:0}.ck3-topology-details dt{color:var(--muted)}.ck3-topology-details dd{margin:0;word-break:break-word}.ck3-topology-badges{display:flex;flex-wrap:wrap;gap:5px;margin:9px 0}.ck3-topology-badge{display:inline-block;padding:1px 7px;border:1px solid var(--line);border-radius:999px;color:var(--muted);font-size:10px}.ck3-topology-badge.center{border-color:var(--accent);color:var(--accent)}.ck3-topology-badge.root{border-color:var(--success);color:var(--success)}.ck3-topology-badge.leaf{border-color:var(--warning);color:var(--warning)}.ck3-topology-badge.cycle{border-color:#cf9eff;color:#cf9eff}.ck3-topology-badge.external{border-color:var(--danger);color:var(--danger)}.ck3-topology-status{min-height:34px;padding:8px 14px;border-top:1px solid var(--line);background:var(--panel);color:var(--muted);font-size:11px}.ck3-topology-node{cursor:pointer;outline:none}.ck3-topology-node rect{fill:#142131;stroke:#5f8db8;stroke-width:2}.ck3-topology-node text{fill:var(--text);font:600 12px "Segoe UI",sans-serif;pointer-events:none}.ck3-topology-node .ck3-topology-subtitle{fill:var(--muted);font-weight:400;font-size:10px}.ck3-topology-node.center rect{stroke:var(--accent);stroke-width:3}.ck3-topology-node.root rect{stroke:var(--success)}.ck3-topology-node.leaf rect{stroke:var(--warning)}.ck3-topology-node.cycle rect{stroke:#cf9eff;stroke-dasharray:5 3}.ck3-topology-node.external rect{fill:#231a20;stroke:var(--danger);stroke-dasharray:6 4}.ck3-topology-node.selected rect{fill:#293c55;stroke:#f7dc70;stroke-width:3}.ck3-topology-node.filtered{display:none}.ck3-topology-edge{fill:none;stroke:#66829f;stroke-width:1.8;marker-end:url(#ck3-topology-arrow);opacity:.88}.ck3-topology-edge.unresolved{stroke:var(--danger);stroke-dasharray:7 5}.ck3-topology-edge.dynamic{stroke:var(--warning);stroke-dasharray:3 4}.ck3-topology-edge.filtered{display:none}.ck3-topology-edge-label{fill:#bdccdb;font:10px "Segoe UI",sans-serif;pointer-events:none}.ck3-topology-edge-label.filtered{display:none}.ck3-topology-marker{fill:#66829f}.ck3-topology-marker.unresolved{fill:var(--danger)}.ck3-topology-marker.dynamic{fill:var(--warning)}.ck3-topology-legend{fill:var(--muted);font:10px "Segoe UI",sans-serif}.ck3-topology-empty{color:var(--muted);padding:15px}@media(max-width:900px){.ck3-topology-workspace{grid-template-columns:1fr;grid-template-rows:minmax(440px,65vh) auto;height:auto}.ck3-topology-canvas-wrap{border-right:0;border-bottom:1px solid var(--line)}.ck3-topology-details{min-height:180px}.ck3-topology-search input{width:180px}}
`

// The generic external style is intentionally neutral. Only an endpoint whose
// edge explicitly says unresolved gets danger styling; a resolved edge omitted
// by a bounded topology is a continuation stub, not a missing definition.
const eventTopologyHTMLInspectorEndpointStyles = `
.ck3-topology-badge.external{border-color:#7894ad;color:#b9c9d8}.ck3-topology-node.external rect{fill:#182432;stroke:#7894ad;stroke-dasharray:6 4}.ck3-topology-node.external.endpoint-unresolved rect{fill:#231a20;stroke:var(--danger)}.ck3-topology-node.external.endpoint-dynamic rect{fill:#272317;stroke:var(--warning)}.ck3-topology-node.external.endpoint-bounded rect{fill:#182432;stroke:#93a7ba;stroke-dasharray:3 5}.ck3-topology-node.external.endpoint-unknown rect{fill:#20202a;stroke:#b6a7d8}
`

// eventTopologyHTMLInspectorScript is intentionally a fixed literal. It only
// reads inert JSON, uses textContent for untrusted values, and has no dynamic
// code evaluation or network API surface.
const eventTopologyHTMLInspectorScript = `(function(){
"use strict";
var payload=document.getElementById("ck3-event-topology-data");
var graph={};try{graph=JSON.parse(payload.textContent||"{}");}catch(_){graph={};}
var nodes=Array.isArray(graph.nodes)?graph.nodes.slice():[];
var edges=Array.isArray(graph.edges)?graph.edges.slice():[];
var center=String(graph.center||"");
var roots=new Set(Array.isArray(graph.roots)?graph.roots:[]);
var leaves=new Set(Array.isArray(graph.leaves)?graph.leaves:[]);
var cycleNodes=new Set();(Array.isArray(graph.cycles)?graph.cycles:[]).forEach(function(c){if(Array.isArray(c)){c.forEach(function(id){cycleNodes.add(id);});}});
var canvas=document.getElementById("ck3-topology-canvas"),scene=document.getElementById("ck3-topology-scene"),details=document.getElementById("ck3-topology-details"),status=document.getElementById("ck3-topology-status"),meta=document.getElementById("ck3-topology-meta");
var search=document.getElementById("ck3-topology-search"),relation=document.getElementById("ck3-topology-relation"),phase=document.getElementById("ck3-topology-phase"),resolution=document.getElementById("ck3-topology-resolution");
var SVG="http://www.w3.org/2000/svg",nodeById=new Map(),externalById=new Map(),positions=new Map(),selected="",view={x:0,y:0,w:1000,h:700},drag=null,filterCache=null;
function string(v){return v===undefined||v===null?"":String(v);}
function lower(v){return string(v).toLowerCase();}
function svg(name,attrs){var el=document.createElementNS(SVG,name);Object.keys(attrs||{}).forEach(function(k){el.setAttribute(k,String(attrs[k]));});return el;}
function appendText(parent,value,attrs){var el=svg("text",attrs||{});el.textContent=string(value);parent.appendChild(el);return el;}
function nodeSearch(n){return lower([n.id,n.name,n.type,n.title,n.source,n.path].join(" "));}
nodes.forEach(function(n){if(n&&n.id!==undefined){nodeById.set(string(n.id),n);}});
function knownNode(id){return nodeById.get(id)||externalById.get(id);}
function edgeResolution(e){return lower(e.resolution||"unknown").trim()||"unknown";}
function endpointKind(edge){var r=edgeResolution(edge);if(r==="unresolved"){return "unresolved";}if(r==="dynamic"){return "dynamic";}if(r==="resolved"&&graph.truncated){return "bounded";}return r==="resolved"?"resolved":"unknown";}
function endpointRank(kind){return kind==="unresolved"?4:kind==="dynamic"?3:kind==="unknown"?2:kind==="bounded"?1:0;}
function endpointLabel(kind){return kind==="unresolved"?"unresolved endpoint":kind==="dynamic"?"dynamic endpoint":kind==="bounded"?"bounded continuation":kind==="resolved"?"returned resolved stub":"unknown-resolution endpoint";}
function externalKind(n){var kind=lower(n&&n.endpoint_kind);return kind==="unresolved"||kind==="dynamic"||kind==="bounded"||kind==="resolved"?kind:"unknown";}
function addExternal(id,edge,fromSide){id=string(id);if(!id||nodeById.has(id)){return;}var kind=endpointKind(edge),existing=externalById.get(id);if(existing){if(endpointRank(kind)>endpointRank(externalKind(existing))){existing.endpoint_kind=kind;existing.resolution=endpointLabel(kind);existing.edge_resolution=edgeResolution(edge);existing.reason=string(edge.reason||"");}return;}var anchor=nodeById.get(fromSide?string(edge.to):string(edge.from));var d=anchor&&Number.isFinite(Number(anchor.distance))?Number(anchor.distance)+1:1;externalById.set(id,{id:id,name:id,type:"external",defined:false,distance:d,external:true,endpoint_kind:kind,resolution:endpointLabel(kind),edge_resolution:edgeResolution(edge),reason:string(edge.reason||""),source:"",path:"",line:0});}
edges.forEach(function(e){if(!e){return;}if(!nodeById.has(string(e.from))){addExternal(e.from,e,true);}if(!nodeById.has(string(e.to))){addExternal(e.to,e,false);}});
var allNodes=nodes.concat(Array.from(externalById.values()));
function distinct(values){return Array.from(new Set(values.filter(function(v){return string(v)!=="";}))).sort();}
function fillSelect(select,label,values){select.replaceChildren();var first=document.createElement("option");first.value="";first.textContent=label;select.appendChild(first);values.forEach(function(v){var option=document.createElement("option");option.value=string(v);option.textContent=string(v);select.appendChild(option);});}
fillSelect(relation,"All relations",distinct(edges.map(function(e){return e&&e.relation;})));
fillSelect(phase,"All phases",distinct(edges.map(function(e){return e&&e.phase;})));
fillSelect(resolution,"All resolutions",distinct(edges.map(function(e){return edgeResolution(e);})));
meta.textContent="center " + (center||"(none)") + " | direction " + string(graph.direction||"both") + " | depth " + string(graph.max_depth||0);
function edgeFilterMatches(e){if(!e){return false;}if(relation.value&&string(e.relation)!==relation.value){return false;}if(phase.value&&string(e.phase)!==phase.value){return false;}if(resolution.value&&edgeResolution(e)!==resolution.value){return false;}return true;}
function edgeFiltersActive(){return Boolean(relation.value||phase.value||resolution.value);}
function nodeMatches(n){var q=lower(search.value).trim();return !q||nodeSearch(n).indexOf(q)>=0;}
function layout(){var lanes=new Map();allNodes.slice().sort(function(a,b){var da=Number(a.distance)||0,db=Number(b.distance)||0;if(da!==db){return da-db;}return string(a.id).localeCompare(string(b.id));}).forEach(function(n){var key=Number.isFinite(Number(n.distance))?Number(n.distance):0;if(!lanes.has(key)){lanes.set(key,[]);}lanes.get(key).push(n);});var maxX=360,maxY=250;lanes.forEach(function(items,d){items.forEach(function(n,i){var x=70+d*300,y=72+i*132;positions.set(string(n.id),{x:x,y:y,w:n.external?220:250,h:78});maxX=Math.max(maxX,x+(n.external?220:250)+90);maxY=Math.max(maxY,y+160);});});return {w:maxX,h:maxY};}
var bounds=layout();
function setView(next){view=next;canvas.setAttribute("viewBox",[view.x.toFixed(2),view.y.toFixed(2),view.w.toFixed(2),view.h.toFixed(2)].join(" "));}
function filteredGraph(){var key=[search.value,relation.value,phase.value,resolution.value].join("\u0000");if(filterCache&&filterCache.key===key){return filterCache;}var candidates=new Set();if(edgeFiltersActive()){edges.forEach(function(e){if(edgeFilterMatches(e)){candidates.add(string(e.from));candidates.add(string(e.to));}});}else{allNodes.forEach(function(n){candidates.add(string(n.id));});}var visible=new Set();allNodes.forEach(function(n){var id=string(n.id);if(candidates.has(id)&&nodeMatches(n)){visible.add(id);}});var matched=new Set();edges.forEach(function(e){if(edgeFilterMatches(e)&&visible.has(string(e.from))&&visible.has(string(e.to))){matched.add(e);}});filterCache={key:key,nodes:visible,edges:matched};return filterCache;}
function visibleIds(){return filteredGraph().nodes;}
function edgeMatches(e){return filteredGraph().edges.has(e);}
function colorFor(value){var h=0;string(value).split("").forEach(function(c){h=((h<<5)-h)+c.charCodeAt(0);h|=0;});return "hsl("+(Math.abs(h)%300+30)+",55%,64%)";}
function nodeClass(n,visible){var classes=["ck3-topology-node"];var id=string(n.id);if(n.external){classes.push("external","endpoint-"+externalKind(n));}if(id===center){classes.push("center");}if(roots.has(id)){classes.push("root");}if(leaves.has(id)){classes.push("leaf");}if(cycleNodes.has(id)){classes.push("cycle");}if(id===selected){classes.push("selected");}if(!visible){classes.push("filtered");}return classes.join(" ");}
function edgeClass(e,visible){var classes=["ck3-topology-edge"];var r=edgeResolution(e);if(r==="unresolved"){classes.push("unresolved");}else if(r==="dynamic"){classes.push("dynamic");}if(!visible){classes.push("filtered");}return classes.join(" ");}
function line(x1,y1,x2,y2){var bend=(x1+x2)/2;return "M"+x1+","+y1+" C"+bend+","+y1+" "+bend+","+y2+" "+x2+","+y2;}
function addDefs(){var defs=svg("defs"),marker=svg("marker",{id:"ck3-topology-arrow",viewBox:"0 0 10 10",refX:"9",refY:"5",markerWidth:"6",markerHeight:"6",orient:"auto-start-reverse"});marker.appendChild(svg("path",{d:"M 0 0 L 10 5 L 0 10 z",class:"ck3-topology-marker"}));defs.appendChild(marker);scene.appendChild(defs);}
function render(){scene.replaceChildren();addDefs();var visible=visibleIds(),matched=edges.filter(edgeMatches);matched.forEach(function(e){var a=positions.get(string(e.from)),b=positions.get(string(e.to));if(!a||!b){return;}var edgeVisible=visible.has(string(e.from))&&visible.has(string(e.to));var path=svg("path",{d:line(a.x+a.w,a.y+a.h/2,b.x,b.y+b.h/2),class:edgeClass(e,edgeVisible),stroke:colorFor(e.relation||"reference")});scene.appendChild(path);var label=string(e.relation||"reference");if(e.phase){label+=" | "+string(e.phase);}if(e.resolution&&e.resolution!=="resolved"){label+=" | "+string(e.resolution);}var tx=(a.x+a.w+b.x)/2,ty=(a.y+a.h/2+b.y+b.h/2)/2-6;var text=appendText(scene,label,{x:tx,y:ty,"text-anchor":"middle",class:"ck3-topology-edge-label"+(edgeVisible?"":" filtered")});text.setAttribute("fill",colorFor(e.relation||"reference"));});allNodes.forEach(function(n){var p=positions.get(string(n.id));if(!p){return;}var g=svg("g",{class:nodeClass(n,visible.has(string(n.id))),transform:"translate("+p.x+","+p.y+")",tabindex:"0",role:"button","aria-label":"Inspect "+string(n.id)});g.appendChild(svg("rect",{width:p.w,height:p.h,rx:"9",ry:"9"}));appendText(g,string(n.id),{x:12,y:28});var subtitle=n.external?(n.resolution||"external endpoint"):[n.type||"event",n.title||""].filter(Boolean).join(" | ");appendText(g,subtitle,{x:12,y:49,class:"ck3-topology-subtitle"});var markers=[];if(string(n.id)===center){markers.push("center");}if(roots.has(string(n.id))){markers.push("root");}if(leaves.has(string(n.id))){markers.push("leaf");}if(cycleNodes.has(string(n.id))){markers.push("cycle");}if(markers.length){appendText(g,markers.join(" / "),{x:12,y:68,class:"ck3-topology-subtitle"});}g.addEventListener("click",function(){selectNode(string(n.id));});g.addEventListener("keydown",function(ev){if(ev.key==="Enter"||ev.key===" "){ev.preventDefault();selectNode(string(n.id));}});scene.appendChild(g);});updateStatus(visible,matched);}
function badge(text,kind){var el=document.createElement("span");el.className="ck3-topology-badge "+kind;el.textContent=text;return el;}
function row(parent,label,value){if(value===undefined||value===null||value===""){return;}var dt=document.createElement("dt"),dd=document.createElement("dd");dt.textContent=label;dd.textContent=string(value);parent.appendChild(dt);parent.appendChild(dd);}
function showEmpty(){details.replaceChildren();var h=document.createElement("h2"),p=document.createElement("p");h.textContent="Select a graph node";p.textContent="Search or filter the returned topology, then select an event, on_action, or derived external endpoint.";details.appendChild(h);details.appendChild(p);}
function showDetails(n){details.replaceChildren();var h=document.createElement("h2");h.textContent=string(n.id);details.appendChild(h);var badges=document.createElement("div");badges.className="ck3-topology-badges";if(string(n.id)===center){badges.appendChild(badge("center","center"));}if(roots.has(string(n.id))){badges.appendChild(badge("root","root"));}if(leaves.has(string(n.id))){badges.appendChild(badge("leaf","leaf"));}if(cycleNodes.has(string(n.id))){badges.appendChild(badge("cycle","cycle"));}if(n.external){badges.appendChild(badge(endpointLabel(externalKind(n)),"external"));}if(badges.childNodes.length){details.appendChild(badges);}var h3=document.createElement("h3");h3.textContent=n.external?"Derived endpoint":"Indexed node";details.appendChild(h3);var dl=document.createElement("dl");row(dl,"Type",n.type);row(dl,"Name",n.name);row(dl,"Title",n.title);row(dl,"Defined",n.external?"not a returned indexed node":String(Boolean(n.defined)));row(dl,"Distance",n.distance);row(dl,"In / out",string(n.in_degree||0)+" / "+string(n.out_degree||0));row(dl,"Source",n.path?(string(n.path)+(n.line?":"+string(n.line):"")):n.source);row(dl,"Resolution",n.resolution);row(dl,"Reason",n.reason);details.appendChild(dl);var related=edges.filter(function(e){return edgeMatches(e)&&(string(e.from)===string(n.id)||string(e.to)===string(n.id));});if(related.length){var rh=document.createElement("h3");rh.textContent="Visible edges ("+related.length+")";details.appendChild(rh);related.slice(0,12).forEach(function(e){var p=document.createElement("p");p.textContent=string(e.from)+" -> "+string(e.to)+" | "+string(e.relation||"reference")+(e.phase?" | "+string(e.phase):"")+(e.resolution&&e.resolution!=="resolved"?" | "+string(e.resolution):"");details.appendChild(p);});}}
function selectNode(id){selected=id;showDetails(knownNode(id)||{id:id,external:true});render();}
function updateStatus(visible,matched){var external=Array.from(externalById.values()).filter(function(n){return visible.has(string(n.id));}).length;var text=visible.size+" visible node(s), "+matched.length+" visible edge(s)";if(external){text+="; "+external+" derived external endpoint(s)";}if(graph.truncated){text+="; topology is bounded/truncated";}text+=". Edge labels preserve relation, phase, and resolution from the returned topology.";status.textContent=text;}
function fit(){var visible=visibleIds(),items=allNodes.filter(function(n){return visible.has(string(n.id));});if(!items.length){setView({x:0,y:0,w:bounds.w,h:bounds.h});return;}var left=Infinity,top=Infinity,right=-Infinity,bottom=-Infinity;items.forEach(function(n){var p=positions.get(string(n.id));if(!p){return;}left=Math.min(left,p.x);top=Math.min(top,p.y);right=Math.max(right,p.x+p.w);bottom=Math.max(bottom,p.y+p.h);});var pad=80;setView({x:left-pad,y:top-pad,w:Math.max(220,right-left+pad*2),h:Math.max(180,bottom-top+pad*2)});}
function zoom(factor){var cx=view.x+view.w/2,cy=view.y+view.h/2,w=Math.max(100,view.w/factor),h=Math.max(80,view.h/factor);setView({x:cx-w/2,y:cy-h/2,w:w,h:h});}
function controlChanged(){selected="";showEmpty();render();fit();}
[search,relation,phase,resolution].forEach(function(el){el.addEventListener(el===search?"input":"change",controlChanged);});
document.getElementById("ck3-topology-fit").addEventListener("click",fit);document.getElementById("ck3-topology-zoom-in").addEventListener("click",function(){zoom(1.25);});document.getElementById("ck3-topology-zoom-out").addEventListener("click",function(){zoom(.8);});
canvas.addEventListener("wheel",function(ev){ev.preventDefault();zoom(ev.deltaY<0?1.18:.85);},{passive:false});canvas.addEventListener("pointerdown",function(ev){if(ev.target===canvas||ev.target===scene){drag={x:ev.clientX,y:ev.clientY,v:{x:view.x,y:view.y,w:view.w,h:view.h}};canvas.setPointerCapture(ev.pointerId);}});canvas.addEventListener("pointermove",function(ev){if(!drag){return;}var box=canvas.getBoundingClientRect(),dx=(ev.clientX-drag.x)*drag.v.w/Math.max(1,box.width),dy=(ev.clientY-drag.y)*drag.v.h/Math.max(1,box.height);setView({x:drag.v.x-dx,y:drag.v.y-dy,w:drag.v.w,h:drag.v.h});});function stopDrag(){drag=null;}canvas.addEventListener("pointerup",stopDrag);canvas.addEventListener("pointercancel",stopDrag);
showEmpty();render();fit();
})();`
