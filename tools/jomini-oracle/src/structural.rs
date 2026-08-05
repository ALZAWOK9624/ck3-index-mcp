//! Lossless structural indexing for a complete Jomini binary token slice.
//!
//! Jomini's binary stream does not encode whether a container is an object, an
//! array, or a mixture of both. This module therefore exposes a *token
//! structure path*, not a guessed semantic object path. A field path segment is
//! identified by the exact decoded key token and its zero-based occurrence
//! among equal raw keys in the same parent container. Values without an equals
//! operator receive a stable zero-based item index in that parent.

use std::{collections::HashMap, error::Error, fmt, mem};

use jomini::binary::{TokenKind, TokenReader, TokenResolver};
use serde::{Deserialize, Serialize};

/// A half-open byte interval in the original binary slice.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct ByteSpan {
    pub start: usize,
    pub end: usize,
}

impl ByteSpan {
    #[must_use]
    pub const fn len(self) -> usize {
        self.end - self.start
    }

    #[must_use]
    pub const fn is_empty(self) -> bool {
        self.start == self.end
    }

    #[must_use]
    pub fn get(self, source: &[u8]) -> Option<&[u8]> {
        source.get(self.start..self.end)
    }
}

/// The binary spelling of a text token.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum TextRepresentation {
    Quoted,
    Unquoted,
}

/// Stable identity for any scalar Jomini binary token.
///
/// Floating-point values are identified by bits rather than host floating
/// point semantics. Text is represented as hexadecimal bytes so invalid UTF-8
/// remains addressable. Resolver output deliberately does not appear here.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum RawTokenIdentity {
    Id {
        token: u16,
    },
    Text {
        representation: TextRepresentation,
        bytes_hex: String,
    },
    U32 {
        value: u32,
    },
    U64 {
        value: u64,
    },
    I32 {
        value: i32,
    },
    I64 {
        value: i64,
    },
    Bool {
        value: bool,
    },
    Lookup {
        index: u32,
    },
    F32 {
        bits_hex: String,
    },
    F64 {
        bits_hex: String,
    },
    Rgb {
        red: u32,
        green: u32,
        blue: u32,
        alpha: Option<u32>,
    },
}

/// A field key plus optional human-readable resolver output.
///
/// `resolved` is display metadata only. Paths and occurrence counters use
/// `raw` exclusively.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct KeyView {
    pub raw: RawTokenIdentity,
    pub resolved: Option<String>,
}

/// One deterministic component of a token structure path.
#[derive(Debug, Clone, PartialEq, Eq, Hash, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case", deny_unknown_fields)]
pub enum PathSegment {
    Field {
        key: RawTokenIdentity,
        occurrence: usize,
    },
    Item {
        index: usize,
    },
}

impl PathSegment {
    /// Serialize this segment without whitespace or unordered maps.
    pub fn canonical_json(&self) -> Result<String, serde_json::Error> {
        serde_json::to_string(self)
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum StructuralEventKind {
    Field,
    Item,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum StructuralValue {
    Scalar {
        raw: RawTokenIdentity,
        /// Optional display-only resolution for identifier and lookup values.
        resolved: Option<String>,
    },
    Container,
}

/// A source-ordered field or anonymous item.
///
/// Container events occur before their descendants in `StructuralDocument`.
/// Their `value_span` includes both the opening and closing token.
#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct StructuralEvent {
    pub kind: StructuralEventKind,
    pub key: Option<KeyView>,
    pub key_span: Option<ByteSpan>,
    pub equal_span: Option<ByteSpan>,
    pub value: StructuralValue,
    pub value_span: ByteSpan,
    /// Parent-container depth. Root fields and items have depth zero.
    pub depth: usize,
    pub path: Vec<PathSegment>,
}

impl StructuralEvent {
    /// Serialize the complete path without whitespace or unordered maps.
    pub fn canonical_path_json(&self) -> Result<String, serde_json::Error> {
        serde_json::to_string(&self.path)
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct StructuralDocument {
    pub source_len: usize,
    pub events: Vec<StructuralEvent>,
}

/// Caller-selected hard limits for one complete structural walk.
///
/// The limits count work performed by this module, rather than sampling host
/// allocator state. In particular, `max_path_segments` counts every path
/// segment copied into a newly-created event path or child-frame path, while
/// `max_dynamic_bytes` conservatively charges owned strings, hash-map keys, and
/// path-vector storage (including owned key strings copied with a path). The
/// latter is cumulative allocation work, not a measurement of peak live heap.
/// Together they catch depth-times-events and long-key-times-descendants
/// amplification that a source-byte limit alone misses.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
pub struct StructuralBudget {
    pub max_source_bytes: u64,
    pub max_tokens: u64,
    pub max_depth: usize,
    pub max_events: u64,
    pub max_path_segments: u64,
    #[serde(default = "unlimited_dynamic_bytes")]
    pub max_dynamic_bytes: u64,
}

const fn unlimited_dynamic_bytes() -> u64 {
    u64::MAX
}

impl StructuralBudget {
    /// Compatibility budget used by the original unbudgeted entry points.
    pub const UNLIMITED: Self = Self {
        max_source_bytes: u64::MAX,
        max_tokens: u64::MAX,
        max_depth: usize::MAX,
        max_events: u64::MAX,
        max_path_segments: u64::MAX,
        max_dynamic_bytes: u64::MAX,
    };
}

/// The resource whose configured structural-walk limit was exceeded.
#[derive(Debug, Clone, Copy, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "snake_case")]
pub enum StructuralResource {
    SourceBytes,
    Tokens,
    Depth,
    Events,
    PathSegments,
    DynamicBytes,
}

impl fmt::Display for StructuralResource {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(match self {
            Self::SourceBytes => "source_bytes",
            Self::Tokens => "tokens",
            Self::Depth => "depth",
            Self::Events => "events",
            Self::PathSegments => "path_segments",
            Self::DynamicBytes => "dynamic_bytes",
        })
    }
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind", rename_all = "snake_case")]
pub enum StructuralErrorKind {
    Reader {
        message: String,
    },
    LimitExceeded {
        resource: StructuralResource,
        limit: u64,
        attempted: u64,
    },
    UnexpectedClose,
    EqualWithoutKey,
    RepeatedEqual,
    FieldMissingValue,
    UnclosedContainers {
        count: usize,
    },
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct StructuralError {
    pub offset: usize,
    pub kind: StructuralErrorKind,
}

impl fmt::Display for StructuralError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "binary structure error at byte {}: ", self.offset)?;
        match &self.kind {
            StructuralErrorKind::Reader { message } => write!(f, "{message}"),
            StructuralErrorKind::LimitExceeded {
                resource,
                limit,
                attempted,
            } => write!(
                f,
                "{resource} limit exceeded (limit {limit}, attempted {attempted})"
            ),
            StructuralErrorKind::UnexpectedClose => write!(f, "unmatched close container"),
            StructuralErrorKind::EqualWithoutKey => {
                write!(f, "equality operator has no field key")
            }
            StructuralErrorKind::RepeatedEqual => write!(f, "repeated equality operator"),
            StructuralErrorKind::FieldMissingValue => write!(f, "field has no value"),
            StructuralErrorKind::UnclosedContainers { count } => {
                write!(f, "{count} container(s) remain open")
            }
        }
    }
}

impl Error for StructuralError {}

#[derive(Debug)]
struct ScalarCandidate {
    raw: RawTokenIdentity,
    resolved: Option<String>,
    span: ByteSpan,
}

#[derive(Debug)]
struct PendingField {
    key: KeyView,
    key_span: ByteSpan,
    equal_span: ByteSpan,
    depth: usize,
    path: Vec<PathSegment>,
}

#[derive(Debug)]
struct BudgetState {
    budget: StructuralBudget,
    tokens: u64,
    events: u64,
    path_segments: u64,
    dynamic_bytes: u64,
}

impl BudgetState {
    fn new(source_len: usize, budget: StructuralBudget) -> Result<Self, StructuralError> {
        let attempted = source_len as u64;
        if attempted > budget.max_source_bytes {
            return Err(StructuralError {
                offset: 0,
                kind: StructuralErrorKind::LimitExceeded {
                    resource: StructuralResource::SourceBytes,
                    limit: budget.max_source_bytes,
                    attempted,
                },
            });
        }
        Ok(Self {
            budget,
            tokens: 0,
            events: 0,
            path_segments: 0,
            dynamic_bytes: 0,
        })
    }

    fn record_token(&mut self, offset: usize) -> Result<(), StructuralError> {
        Self::record(
            &mut self.tokens,
            1,
            self.budget.max_tokens,
            StructuralResource::Tokens,
            offset,
        )
    }

    fn require_depth(&self, depth: usize, offset: usize) -> Result<(), StructuralError> {
        if depth > self.budget.max_depth {
            return Err(StructuralError {
                offset,
                kind: StructuralErrorKind::LimitExceeded {
                    resource: StructuralResource::Depth,
                    limit: self.budget.max_depth as u64,
                    attempted: depth as u64,
                },
            });
        }
        Ok(())
    }

    fn record_event(&mut self, offset: usize) -> Result<(), StructuralError> {
        Self::record(
            &mut self.events,
            1,
            self.budget.max_events,
            StructuralResource::Events,
            offset,
        )
    }

    fn record_path_clone(&mut self, segments: usize, offset: usize) -> Result<(), StructuralError> {
        Self::record(
            &mut self.path_segments,
            segments as u64,
            self.budget.max_path_segments,
            StructuralResource::PathSegments,
            offset,
        )
    }

    fn record_dynamic_bytes(&mut self, bytes: u64, offset: usize) -> Result<(), StructuralError> {
        Self::record(
            &mut self.dynamic_bytes,
            bytes,
            self.budget.max_dynamic_bytes,
            StructuralResource::DynamicBytes,
            offset,
        )
    }

    fn record(
        current: &mut u64,
        amount: u64,
        limit: u64,
        resource: StructuralResource,
        offset: usize,
    ) -> Result<(), StructuralError> {
        let attempted = current.saturating_add(amount);
        if attempted > limit {
            return Err(StructuralError {
                offset,
                kind: StructuralErrorKind::LimitExceeded {
                    resource,
                    limit,
                    attempted,
                },
            });
        }
        *current = attempted;
        Ok(())
    }
}

#[derive(Debug)]
struct Frame {
    path: Vec<PathSegment>,
    key_occurrences: HashMap<RawTokenIdentity, usize>,
    next_item: usize,
    container_event: Option<usize>,
}

impl Frame {
    fn root() -> Self {
        Self {
            path: Vec::new(),
            key_occurrences: HashMap::new(),
            next_item: 0,
            container_event: None,
        }
    }

    fn field_path(
        &mut self,
        key: &RawTokenIdentity,
        state: &mut BudgetState,
        offset: usize,
    ) -> Result<Vec<PathSegment>, StructuralError> {
        let new_len = self.path.len().saturating_add(1);
        state.record_path_clone(new_len, offset)?;
        let new_hash_key = !self.key_occurrences.contains_key(key);
        let dynamic_bytes =
            extended_field_path_dynamic_bytes(&self.path, key).saturating_add(if new_hash_key {
                hash_entry_dynamic_bytes(key)
            } else {
                0
            });
        state.record_dynamic_bytes(dynamic_bytes, offset)?;

        let occurrence = if new_hash_key {
            self.key_occurrences.insert(key.clone(), 1);
            0
        } else {
            let next = self
                .key_occurrences
                .get_mut(key)
                .expect("the key was observed immediately before mutation");
            let occurrence = *next;
            *next = next.saturating_add(1);
            occurrence
        };

        let mut path = Vec::with_capacity(new_len);
        path.extend(self.path.iter().cloned());
        path.push(PathSegment::Field {
            key: key.clone(),
            occurrence,
        });
        Ok(path)
    }

    fn item_path(
        &mut self,
        state: &mut BudgetState,
        offset: usize,
    ) -> Result<Vec<PathSegment>, StructuralError> {
        let new_len = self.path.len().saturating_add(1);
        state.record_path_clone(new_len, offset)?;
        state.record_dynamic_bytes(extended_item_path_dynamic_bytes(&self.path), offset)?;
        let index = self.next_item;
        self.next_item += 1;

        let mut path = Vec::with_capacity(new_len);
        path.extend(self.path.iter().cloned());
        path.push(PathSegment::Item { index });
        Ok(path)
    }
}

/// Charges one owned hash-table key plus spare bucket/control capacity. The
/// two-times factor is deliberately conservative for a load-factor-managed
/// table; the owned string payload is included in each key clone.
fn hash_entry_dynamic_bytes(key: &RawTokenIdentity) -> u64 {
    let inline = mem::size_of::<(RawTokenIdentity, usize)>() as u64;
    inline
        .saturating_add(raw_identity_heap_bytes(key))
        .saturating_mul(2)
}

fn raw_identity_heap_bytes(raw: &RawTokenIdentity) -> u64 {
    match raw {
        RawTokenIdentity::Text { bytes_hex, .. } => bytes_hex.len() as u64,
        RawTokenIdentity::F32 { bits_hex } | RawTokenIdentity::F64 { bits_hex } => {
            bits_hex.len() as u64
        }
        RawTokenIdentity::Id { .. }
        | RawTokenIdentity::U32 { .. }
        | RawTokenIdentity::U64 { .. }
        | RawTokenIdentity::I32 { .. }
        | RawTokenIdentity::I64 { .. }
        | RawTokenIdentity::Bool { .. }
        | RawTokenIdentity::Lookup { .. }
        | RawTokenIdentity::Rgb { .. } => 0,
    }
}

fn path_segment_dynamic_bytes(segment: &PathSegment) -> u64 {
    let inline = mem::size_of::<PathSegment>() as u64;
    match segment {
        PathSegment::Field { key, .. } => inline.saturating_add(raw_identity_heap_bytes(key)),
        PathSegment::Item { .. } => inline,
    }
}

fn path_clone_dynamic_bytes(path: &[PathSegment]) -> u64 {
    path.iter().fold(0u64, |total, segment| {
        total.saturating_add(path_segment_dynamic_bytes(segment))
    })
}

fn extended_field_path_dynamic_bytes(parent: &[PathSegment], key: &RawTokenIdentity) -> u64 {
    path_clone_dynamic_bytes(parent)
        .saturating_add(mem::size_of::<PathSegment>() as u64)
        .saturating_add(raw_identity_heap_bytes(key))
}

fn extended_item_path_dynamic_bytes(parent: &[PathSegment]) -> u64 {
    path_clone_dynamic_bytes(parent).saturating_add(mem::size_of::<PathSegment>() as u64)
}

fn clone_path_with_budget(
    path: &[PathSegment],
    state: &mut BudgetState,
    offset: usize,
) -> Result<Vec<PathSegment>, StructuralError> {
    state.record_path_clone(path.len(), offset)?;
    state.record_dynamic_bytes(path_clone_dynamic_bytes(path), offset)?;
    let mut cloned = Vec::with_capacity(path.len());
    cloned.extend(path.iter().cloned());
    Ok(cloned)
}

/// Walk a complete binary token slice without resolving token identifiers.
pub fn walk_binary(source: &[u8]) -> Result<StructuralDocument, StructuralError> {
    walk_binary_with_budget(source, StructuralBudget::UNLIMITED)
}

/// Walk a complete binary token slice under caller-selected hard limits.
pub fn walk_binary_with_budget(
    source: &[u8],
    budget: StructuralBudget,
) -> Result<StructuralDocument, StructuralError> {
    walk_binary_with_resolver_and_budget(source, None, budget)
}

/// Walk a complete binary token slice with optional display-only resolution.
///
/// Every source byte must be a valid token and every explicit container must be
/// closed. The function returns no partial document on malformed input.
pub fn walk_binary_with_resolver(
    source: &[u8],
    resolver: Option<&dyn TokenResolver>,
) -> Result<StructuralDocument, StructuralError> {
    walk_binary_with_resolver_and_budget(source, resolver, StructuralBudget::UNLIMITED)
}

/// Walk a complete binary token slice with display-only resolution and hard
/// caller-selected resource limits.
///
/// Source size is rejected before reader/frame/event allocation. Token, depth,
/// event, and cloned-path limits are checked before the corresponding owned
/// structural value is allocated or appended.
pub fn walk_binary_with_resolver_and_budget(
    source: &[u8],
    resolver: Option<&dyn TokenResolver>,
    budget: StructuralBudget,
) -> Result<StructuralDocument, StructuralError> {
    let mut budget_state = BudgetState::new(source.len(), budget)?;
    let mut reader = TokenReader::from_slice(source);
    let mut frames = vec![Frame::root()];
    let mut events = Vec::new();
    let mut pending_scalar = None::<ScalarCandidate>;
    let mut pending_field = None::<PendingField>;

    loop {
        let start = reader.position();
        let token = match reader.next_token() {
            Ok(Some(token)) => token,
            Ok(None) => break,
            Err(error) => {
                return Err(StructuralError {
                    offset: reader.position(),
                    kind: StructuralErrorKind::Reader {
                        message: error.to_string(),
                    },
                });
            }
        };
        let span = ByteSpan {
            start,
            end: reader.position(),
        };
        budget_state.record_token(start)?;

        match token {
            TokenKind::Equal => {
                if pending_field.is_some() {
                    return Err(StructuralError {
                        offset: start,
                        kind: StructuralErrorKind::RepeatedEqual,
                    });
                }
                let candidate = pending_scalar.take().ok_or(StructuralError {
                    offset: start,
                    kind: StructuralErrorKind::EqualWithoutKey,
                })?;
                let frame = frames.last_mut().expect("root frame is never removed");
                let path = frame.field_path(&candidate.raw, &mut budget_state, start)?;
                pending_field = Some(PendingField {
                    key: KeyView {
                        raw: candidate.raw,
                        resolved: candidate.resolved,
                    },
                    key_span: candidate.span,
                    equal_span: span,
                    depth: frames.len() - 1,
                    path,
                });
            }
            TokenKind::Open => {
                budget_state.require_depth(frames.len(), start)?;
                if pending_field.is_none()
                    && let Some(candidate) = pending_scalar.take()
                {
                    push_anonymous_scalar(
                        &mut events,
                        &mut frames,
                        candidate,
                        &mut budget_state,
                        start,
                    )?;
                }

                budget_state.record_event(start)?;
                let (event, child_path) = if let Some(field) = pending_field.take() {
                    let child_path = clone_path_with_budget(&field.path, &mut budget_state, start)?;
                    (
                        StructuralEvent {
                            kind: StructuralEventKind::Field,
                            key: Some(field.key),
                            key_span: Some(field.key_span),
                            equal_span: Some(field.equal_span),
                            value: StructuralValue::Container,
                            value_span: span,
                            depth: field.depth,
                            path: field.path,
                        },
                        child_path,
                    )
                } else {
                    let frame = frames.last_mut().expect("root frame is never removed");
                    let path = frame.item_path(&mut budget_state, start)?;
                    let child_path = clone_path_with_budget(&path, &mut budget_state, start)?;
                    (
                        StructuralEvent {
                            kind: StructuralEventKind::Item,
                            key: None,
                            key_span: None,
                            equal_span: None,
                            value: StructuralValue::Container,
                            value_span: span,
                            depth: frames.len() - 1,
                            path,
                        },
                        child_path,
                    )
                };

                let container_event = events.len();
                events.push(event);
                frames.push(Frame {
                    path: child_path,
                    key_occurrences: HashMap::new(),
                    next_item: 0,
                    container_event: Some(container_event),
                });
            }
            TokenKind::Close => {
                if pending_field.is_some() {
                    return Err(StructuralError {
                        offset: start,
                        kind: StructuralErrorKind::FieldMissingValue,
                    });
                }
                if frames.len() == 1 {
                    return Err(StructuralError {
                        offset: start,
                        kind: StructuralErrorKind::UnexpectedClose,
                    });
                }
                if let Some(candidate) = pending_scalar.take() {
                    push_anonymous_scalar(
                        &mut events,
                        &mut frames,
                        candidate,
                        &mut budget_state,
                        start,
                    )?;
                }

                let frame = frames.pop().expect("non-root frame exists");
                let event_index = frame
                    .container_event
                    .expect("only the root frame lacks a container event");
                events[event_index].value_span.end = span.end;
            }
            scalar => {
                if pending_field.is_some() {
                    budget_state.record_event(start)?;
                    let candidate = scalar_candidate(
                        scalar,
                        &reader,
                        source,
                        span,
                        resolver,
                        &mut budget_state,
                        start,
                    )?
                    .expect("open, close, and equal were handled above");
                    let field = pending_field
                        .take()
                        .expect("pending field presence was checked above");
                    events.push(StructuralEvent {
                        kind: StructuralEventKind::Field,
                        key: Some(field.key),
                        key_span: Some(field.key_span),
                        equal_span: Some(field.equal_span),
                        value: StructuralValue::Scalar {
                            raw: candidate.raw,
                            resolved: candidate.resolved,
                        },
                        value_span: candidate.span,
                        depth: field.depth,
                        path: field.path,
                    });
                } else {
                    if let Some(previous) = pending_scalar.take() {
                        push_anonymous_scalar(
                            &mut events,
                            &mut frames,
                            previous,
                            &mut budget_state,
                            start,
                        )?;
                    }
                    let candidate = scalar_candidate(
                        scalar,
                        &reader,
                        source,
                        span,
                        resolver,
                        &mut budget_state,
                        start,
                    )?
                    .expect("open, close, and equal were handled above");
                    pending_scalar = Some(candidate);
                }
            }
        }
    }

    if pending_field.is_some() {
        return Err(StructuralError {
            offset: source.len(),
            kind: StructuralErrorKind::FieldMissingValue,
        });
    }
    if let Some(candidate) = pending_scalar.take() {
        push_anonymous_scalar(
            &mut events,
            &mut frames,
            candidate,
            &mut budget_state,
            source.len(),
        )?;
    }
    if frames.len() != 1 {
        return Err(StructuralError {
            offset: source.len(),
            kind: StructuralErrorKind::UnclosedContainers {
                count: frames.len() - 1,
            },
        });
    }

    Ok(StructuralDocument {
        source_len: source.len(),
        events,
    })
}

fn push_anonymous_scalar(
    events: &mut Vec<StructuralEvent>,
    frames: &mut [Frame],
    candidate: ScalarCandidate,
    budget_state: &mut BudgetState,
    offset: usize,
) -> Result<(), StructuralError> {
    budget_state.record_event(offset)?;
    let depth = frames.len() - 1;
    let frame = frames.last_mut().expect("root frame is never removed");
    let path = frame.item_path(budget_state, offset)?;
    events.push(StructuralEvent {
        kind: StructuralEventKind::Item,
        key: None,
        key_span: None,
        equal_span: None,
        value: StructuralValue::Scalar {
            raw: candidate.raw,
            resolved: candidate.resolved,
        },
        value_span: candidate.span,
        depth,
        path,
    });
    Ok(())
}

fn scalar_candidate(
    token: TokenKind,
    reader: &TokenReader<'_>,
    source: &[u8],
    span: ByteSpan,
    resolver: Option<&dyn TokenResolver>,
    budget_state: &mut BudgetState,
    offset: usize,
) -> Result<Option<ScalarCandidate>, StructuralError> {
    let (raw, resolved) = match token {
        TokenKind::Id => {
            let token = reader.token_id();
            (
                RawTokenIdentity::Id { token },
                budgeted_resolved_string(
                    resolver.and_then(|resolver| resolver.resolve(token)),
                    budget_state,
                    offset,
                )?,
            )
        }
        TokenKind::Quoted => (
            RawTokenIdentity::Text {
                representation: TextRepresentation::Quoted,
                bytes_hex: budgeted_bytes_hex(text_payload(source, span), budget_state, offset)?,
            },
            None,
        ),
        TokenKind::Unquoted => (
            RawTokenIdentity::Text {
                representation: TextRepresentation::Unquoted,
                bytes_hex: budgeted_bytes_hex(text_payload(source, span), budget_state, offset)?,
            },
            None,
        ),
        TokenKind::U32 => (
            RawTokenIdentity::U32 {
                value: reader.u32_data(),
            },
            None,
        ),
        TokenKind::U64 => (
            RawTokenIdentity::U64 {
                value: reader.u64_data(),
            },
            None,
        ),
        TokenKind::I32 => (
            RawTokenIdentity::I32 {
                value: reader.i32_data(),
            },
            None,
        ),
        TokenKind::I64 => (
            RawTokenIdentity::I64 {
                value: reader.i64_data(),
            },
            None,
        ),
        TokenKind::Bool => (
            RawTokenIdentity::Bool {
                value: reader.bool_data(),
            },
            None,
        ),
        TokenKind::Lookup => {
            let index = reader.lookup_data();
            (
                RawTokenIdentity::Lookup { index },
                budgeted_resolved_string(
                    resolver.and_then(|resolver| resolver.lookup(index)),
                    budget_state,
                    offset,
                )?,
            )
        }
        TokenKind::F32 => (
            RawTokenIdentity::F32 {
                bits_hex: budgeted_bytes_hex(&reader.f32_data(), budget_state, offset)?,
            },
            None,
        ),
        TokenKind::F64 => (
            RawTokenIdentity::F64 {
                bits_hex: budgeted_bytes_hex(&reader.f64_data(), budget_state, offset)?,
            },
            None,
        ),
        TokenKind::Rgb => {
            let rgb = reader.rgb_data();
            (
                RawTokenIdentity::Rgb {
                    red: rgb.r,
                    green: rgb.g,
                    blue: rgb.b,
                    alpha: rgb.a,
                },
                None,
            )
        }
        TokenKind::Open | TokenKind::Close | TokenKind::Equal => return Ok(None),
    };

    Ok(Some(ScalarCandidate {
        raw,
        resolved,
        span,
    }))
}

fn text_payload(source: &[u8], span: ByteSpan) -> &[u8] {
    if span.len() == 2 {
        // Jomini's dedicated empty-string lexeme has no length or payload.
        &[]
    } else {
        &source[span.start + 4..span.end]
    }
}

fn bytes_hex(bytes: &[u8]) -> String {
    use fmt::Write as _;

    let mut result = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        write!(result, "{byte:02x}").expect("writing to a String cannot fail");
    }
    result
}

fn budgeted_bytes_hex(
    bytes: &[u8],
    budget_state: &mut BudgetState,
    offset: usize,
) -> Result<String, StructuralError> {
    let output_bytes = (bytes.len() as u64).saturating_mul(2);
    budget_state.record_dynamic_bytes(output_bytes, offset)?;
    Ok(bytes_hex(bytes))
}

fn budgeted_resolved_string(
    value: Option<&str>,
    budget_state: &mut BudgetState,
    offset: usize,
) -> Result<Option<String>, StructuralError> {
    let Some(value) = value else {
        return Ok(None);
    };
    budget_state.record_dynamic_bytes(value.len() as u64, offset)?;
    Ok(Some(value.to_owned()))
}

#[cfg(test)]
mod tests {
    use std::collections::HashMap;

    use jomini::{
        Scalar,
        binary::{Rgb, Token, TokenReader},
    };

    use super::*;

    fn encode(tokens: &[Token<'_>]) -> (Vec<u8>, Vec<ByteSpan>) {
        let mut bytes = Vec::new();
        let mut spans = Vec::with_capacity(tokens.len());
        for token in tokens {
            let start = bytes.len();
            token.write(&mut bytes).expect("token should encode");
            spans.push(ByteSpan {
                start,
                end: bytes.len(),
            });
        }
        (bytes, spans)
    }

    fn field_segment(key: RawTokenIdentity, occurrence: usize) -> PathSegment {
        PathSegment::Field { key, occurrence }
    }

    fn generous_budget(source: &[u8]) -> StructuralBudget {
        StructuralBudget {
            max_source_bytes: source.len() as u64,
            max_tokens: u64::MAX,
            max_depth: usize::MAX,
            max_events: u64::MAX,
            max_path_segments: u64::MAX,
            max_dynamic_bytes: u64::MAX,
        }
    }

    fn assert_limit(
        error: StructuralError,
        offset: usize,
        resource: StructuralResource,
        limit: u64,
        attempted: u64,
    ) {
        assert_eq!(
            error,
            StructuralError {
                offset,
                kind: StructuralErrorKind::LimitExceeded {
                    resource,
                    limit,
                    attempted,
                },
            }
        );
    }

    #[test]
    fn budget_boundaries_are_inclusive_and_each_resource_is_distinct() {
        let (source, spans) = encode(&[Token::Id(0x1001), Token::Equal, Token::U32(7)]);
        let exact = StructuralBudget {
            max_source_bytes: source.len() as u64,
            max_tokens: 3,
            max_depth: 0,
            max_events: 1,
            max_path_segments: 1,
            max_dynamic_bytes: u64::MAX,
        };
        assert_eq!(
            walk_binary_with_budget(&source, exact)
                .expect("an exact inclusive budget should pass")
                .events
                .len(),
            1
        );

        let mut limited = exact;
        limited.max_source_bytes -= 1;
        assert_limit(
            walk_binary_with_budget(&source, limited).unwrap_err(),
            0,
            StructuralResource::SourceBytes,
            limited.max_source_bytes,
            source.len() as u64,
        );

        let mut limited = exact;
        limited.max_tokens = 2;
        assert_limit(
            walk_binary_with_budget(&source, limited).unwrap_err(),
            spans[2].start,
            StructuralResource::Tokens,
            2,
            3,
        );

        let mut limited = exact;
        limited.max_events = 0;
        assert_limit(
            walk_binary_with_budget(&source, limited).unwrap_err(),
            spans[2].start,
            StructuralResource::Events,
            0,
            1,
        );

        let mut limited = exact;
        limited.max_path_segments = 0;
        assert_limit(
            walk_binary_with_budget(&source, limited).unwrap_err(),
            spans[1].start,
            StructuralResource::PathSegments,
            0,
            1,
        );

        let (container, container_spans) = encode(&[Token::Open, Token::Close]);
        let mut limited = generous_budget(&container);
        limited.max_depth = 0;
        assert_limit(
            walk_binary_with_budget(&container, limited).unwrap_err(),
            container_spans[0].start,
            StructuralResource::Depth,
            0,
            1,
        );
    }

    #[test]
    fn child_frame_path_clone_is_budgeted_before_it_is_created() {
        let (source, spans) = encode(&[Token::Open, Token::Close]);
        let exact = StructuralBudget {
            max_source_bytes: source.len() as u64,
            max_tokens: 2,
            max_depth: 1,
            max_events: 1,
            // One segment for the container event and one for its child frame.
            max_path_segments: 2,
            max_dynamic_bytes: u64::MAX,
        };
        assert!(walk_binary_with_budget(&source, exact).is_ok());

        let mut limited = exact;
        limited.max_path_segments = 1;
        assert_limit(
            walk_binary_with_budget(&source, limited).unwrap_err(),
            spans[0].start,
            StructuralResource::PathSegments,
            1,
            2,
        );
    }

    #[test]
    fn long_text_key_descendant_amplification_has_an_exact_dynamic_boundary() {
        const KEY_BYTES: usize = 4 * 1024;
        const DESCENDANTS: usize = 64;

        let long_key = vec![b'k'; KEY_BYTES];
        let mut tokens = Vec::with_capacity(4 + DESCENDANTS * 3);
        tokens.extend([
            Token::Quoted(Scalar::new(&long_key)),
            Token::Equal,
            Token::Open,
        ]);
        for value in 0..DESCENDANTS {
            tokens.extend([Token::Id(0x2000), Token::Equal, Token::U32(value as u32)]);
        }
        tokens.push(Token::Close);
        let (source, spans) = encode(&tokens);

        let long_raw = RawTokenIdentity::Text {
            representation: TextRepresentation::Quoted,
            bytes_hex: "6b".repeat(KEY_BYTES),
        };
        let child_raw = RawTokenIdentity::Id { token: 0x2000 };
        let root_path = vec![field_segment(long_raw.clone(), 0)];
        let child_path = vec![
            field_segment(long_raw.clone(), 0),
            field_segment(child_raw.clone(), 0),
        ];
        let exact_dynamic_bytes = (KEY_BYTES as u64)
            .saturating_mul(2)
            .saturating_add(hash_entry_dynamic_bytes(&long_raw))
            .saturating_add(path_clone_dynamic_bytes(&root_path))
            .saturating_add(path_clone_dynamic_bytes(&root_path))
            .saturating_add(hash_entry_dynamic_bytes(&child_raw))
            .saturating_add(
                path_clone_dynamic_bytes(&child_path).saturating_mul(DESCENDANTS as u64),
            );
        assert!(
            exact_dynamic_bytes > (source.len() as u64).saturating_mul(64),
            "the fixture should amplify a small source into much larger cumulative path storage"
        );

        let exact = StructuralBudget {
            max_source_bytes: source.len() as u64,
            max_tokens: tokens.len() as u64,
            max_depth: 1,
            max_events: (DESCENDANTS + 1) as u64,
            max_path_segments: (2 + DESCENDANTS * 2) as u64,
            max_dynamic_bytes: exact_dynamic_bytes,
        };
        assert_eq!(
            walk_binary_with_budget(&source, exact)
                .expect("the exact cumulative dynamic-byte boundary should pass")
                .events
                .len(),
            DESCENDANTS + 1
        );

        let mut limited = exact;
        limited.max_dynamic_bytes -= 1;
        let final_equal = 4 + (DESCENDANTS - 1) * 3;
        assert_limit(
            walk_binary_with_budget(&source, limited).unwrap_err(),
            spans[final_equal].start,
            StructuralResource::DynamicBytes,
            limited.max_dynamic_bytes,
            exact_dynamic_bytes,
        );
    }

    #[test]
    fn resolver_strings_are_charged_before_becoming_owned() {
        struct Resolver;

        impl TokenResolver for Resolver {
            fn resolve(&self, token: u16) -> Option<&str> {
                (token == 0x1001).then_some("a deliberately long resolved display name")
            }

            fn lookup(&self, _index: u32) -> Option<&str> {
                None
            }
        }

        let (source, spans) = encode(&[Token::Id(0x1001), Token::Equal, Token::U32(7)]);
        let resolved_bytes = "a deliberately long resolved display name".len() as u64;
        let mut budget = generous_budget(&source);
        budget.max_dynamic_bytes = resolved_bytes - 1;
        assert_limit(
            walk_binary_with_resolver_and_budget(&source, Some(&Resolver), budget).unwrap_err(),
            spans[0].start,
            StructuralResource::DynamicBytes,
            resolved_bytes - 1,
            resolved_bytes,
        );
    }

    #[test]
    fn malicious_deep_nesting_stops_at_the_first_disallowed_open() {
        const ALLOWED_DEPTH: usize = 32;
        const ATTACK_DEPTH: usize = 1_024;
        let mut tokens = vec![Token::Open; ATTACK_DEPTH];
        tokens.extend(std::iter::repeat_n(Token::Close, ATTACK_DEPTH));
        let (source, spans) = encode(&tokens);
        let mut budget = generous_budget(&source);
        budget.max_depth = ALLOWED_DEPTH;

        assert_limit(
            walk_binary_with_budget(&source, budget).unwrap_err(),
            spans[ALLOWED_DEPTH].start,
            StructuralResource::Depth,
            ALLOWED_DEPTH as u64,
            (ALLOWED_DEPTH + 1) as u64,
        );
    }

    #[test]
    fn caller_can_authorize_a_large_flat_document_without_a_hidden_cap() {
        const EVENTS: usize = 10_000;
        let mut tokens = Vec::with_capacity(EVENTS * 3);
        for value in 0..EVENTS {
            tokens.extend([Token::Id(0x1001), Token::Equal, Token::U32(value as u32)]);
        }
        let (source, _) = encode(&tokens);
        let budget = StructuralBudget {
            max_source_bytes: source.len() as u64,
            max_tokens: (EVENTS * 3) as u64,
            max_depth: 0,
            max_events: EVENTS as u64,
            max_path_segments: EVENTS as u64,
            max_dynamic_bytes: u64::MAX,
        };

        let document = walk_binary_with_budget(&source, budget)
            .expect("caller-supplied limits should permit the complete document");
        assert_eq!(document.events.len(), EVENTS);
    }

    #[test]
    fn nested_fields_and_repeated_keys_have_parent_local_occurrences() {
        let tokens = [
            Token::Id(0x10),
            Token::Equal,
            Token::Open,
            Token::Id(0x20),
            Token::Equal,
            Token::U32(1),
            Token::Id(0x20),
            Token::Equal,
            Token::U32(2),
            Token::Id(0x30),
            Token::Equal,
            Token::Open,
            Token::Id(0x20),
            Token::Equal,
            Token::U32(3),
            Token::Close,
            Token::Close,
            Token::Id(0x10),
            Token::Equal,
            Token::Bool(true),
        ];
        let (source, spans) = encode(&tokens);
        let document = walk_binary(&source).expect("structure should parse");

        assert_eq!(document.events.len(), 6);
        let key_10 = RawTokenIdentity::Id { token: 0x10 };
        let key_20 = RawTokenIdentity::Id { token: 0x20 };
        let key_30 = RawTokenIdentity::Id { token: 0x30 };

        assert_eq!(
            document.events[0].path,
            vec![field_segment(key_10.clone(), 0)]
        );
        assert_eq!(document.events[0].value_span.start, spans[2].start);
        assert_eq!(document.events[0].value_span.end, spans[16].end);
        assert_eq!(document.events[0].depth, 0);

        assert_eq!(
            document.events[1].path,
            vec![
                field_segment(key_10.clone(), 0),
                field_segment(key_20.clone(), 0),
            ]
        );
        assert_eq!(document.events[1].key_span, Some(spans[3]));
        assert_eq!(document.events[1].value_span, spans[5]);
        assert_eq!(document.events[1].depth, 1);
        assert_eq!(
            document.events[2].path,
            vec![
                field_segment(key_10.clone(), 0),
                field_segment(key_20.clone(), 1),
            ]
        );

        assert_eq!(
            document.events[3].path,
            vec![field_segment(key_10.clone(), 0), field_segment(key_30, 0),]
        );
        assert_eq!(document.events[3].value_span.start, spans[11].start);
        assert_eq!(document.events[3].value_span.end, spans[15].end);
        assert_eq!(
            document.events[4].path,
            vec![
                field_segment(key_10.clone(), 0),
                field_segment(RawTokenIdentity::Id { token: 0x30 }, 0),
                field_segment(key_20, 0),
            ]
        );
        assert_eq!(document.events[5].path, vec![field_segment(key_10, 1)]);
    }

    #[test]
    fn anonymous_scalars_and_containers_use_stable_item_indices() {
        let tokens = [
            Token::Open,
            Token::U32(7),
            Token::Open,
            Token::Quoted(Scalar::new(b"leaf")),
            Token::Close,
            Token::Id(0x1001),
            Token::Equal,
            Token::U32(9),
            Token::Close,
        ];
        let (source, spans) = encode(&tokens);
        let document = walk_binary(&source).expect("mixed structure should parse");

        assert_eq!(document.events.len(), 5);
        assert_eq!(
            document.events[0].path,
            vec![PathSegment::Item { index: 0 }]
        );
        assert_eq!(document.events[0].value_span.end, spans[8].end);
        assert_eq!(
            document.events[1].path,
            vec![
                PathSegment::Item { index: 0 },
                PathSegment::Item { index: 0 }
            ]
        );
        assert_eq!(
            document.events[2].path,
            vec![
                PathSegment::Item { index: 0 },
                PathSegment::Item { index: 1 }
            ]
        );
        assert_eq!(document.events[2].value_span.start, spans[2].start);
        assert_eq!(document.events[2].value_span.end, spans[4].end);
        assert_eq!(
            document.events[3].path,
            vec![
                PathSegment::Item { index: 0 },
                PathSegment::Item { index: 1 },
                PathSegment::Item { index: 0 },
            ]
        );
        assert_eq!(
            document.events[4].path,
            vec![
                PathSegment::Item { index: 0 },
                field_segment(RawTokenIdentity::Id { token: 0x1001 }, 0),
            ]
        );
    }

    #[test]
    fn every_scalar_kind_can_be_a_raw_field_key() {
        let keys = [
            Token::Id(0x1234),
            Token::Quoted(Scalar::new(&[0xff, b'a'])),
            Token::Unquoted(Scalar::new(b"name")),
            Token::U32(u32::MAX),
            Token::U64(u64::MAX),
            Token::I32(-12),
            Token::I64(-34),
            Token::Bool(true),
            Token::Lookup(0x12_3456),
            Token::F32([1, 2, 3, 4]),
            Token::F64([1, 2, 3, 4, 5, 6, 7, 8]),
            Token::Rgb(Rgb {
                r: 10,
                g: 20,
                b: 30,
                a: Some(40),
            }),
        ];
        let mut tokens = Vec::new();
        for (index, key) in keys.iter().enumerate() {
            tokens.extend([*key, Token::Equal, Token::U32(index as u32)]);
        }
        let (source, spans) = encode(&tokens);
        let document = walk_binary(&source).expect("all scalar keys should parse");

        let expected = [
            RawTokenIdentity::Id { token: 0x1234 },
            RawTokenIdentity::Text {
                representation: TextRepresentation::Quoted,
                bytes_hex: "ff61".to_owned(),
            },
            RawTokenIdentity::Text {
                representation: TextRepresentation::Unquoted,
                bytes_hex: "6e616d65".to_owned(),
            },
            RawTokenIdentity::U32 { value: u32::MAX },
            RawTokenIdentity::U64 { value: u64::MAX },
            RawTokenIdentity::I32 { value: -12 },
            RawTokenIdentity::I64 { value: -34 },
            RawTokenIdentity::Bool { value: true },
            RawTokenIdentity::Lookup { index: 0x12_3456 },
            RawTokenIdentity::F32 {
                bits_hex: "01020304".to_owned(),
            },
            RawTokenIdentity::F64 {
                bits_hex: "0102030405060708".to_owned(),
            },
            RawTokenIdentity::Rgb {
                red: 10,
                green: 20,
                blue: 30,
                alpha: Some(40),
            },
        ];

        assert_eq!(document.events.len(), expected.len());
        for (index, (event, raw)) in document.events.iter().zip(expected).enumerate() {
            assert_eq!(event.key.as_ref().map(|key| &key.raw), Some(&raw));
            assert_eq!(event.key_span, Some(spans[index * 3]));
            assert_eq!(event.equal_span, Some(spans[index * 3 + 1]));
            assert_eq!(event.value_span, spans[index * 3 + 2]);
        }
    }

    #[test]
    fn quoted_key_and_value_spans_are_exact() {
        let tokens = [
            Token::Quoted(Scalar::new(b"a=b\0")),
            Token::Equal,
            Token::Quoted(Scalar::new(&[0, 1, 0xff])),
        ];
        let (source, spans) = encode(&tokens);
        let document = walk_binary(&source).expect("quoted field should parse");
        let event = &document.events[0];

        assert_eq!(event.key_span, Some(spans[0]));
        assert_eq!(event.equal_span, Some(spans[1]));
        assert_eq!(event.value_span, spans[2]);
        assert_eq!(
            spans[0].get(&source),
            Some(&source[spans[0].start..spans[0].end])
        );
        assert_eq!(
            spans[2].get(&source),
            Some(&source[spans[2].start..spans[2].end])
        );
        assert_eq!(
            event.canonical_path_json().unwrap(),
            r#"[{"kind":"field","key":{"kind":"text","representation":"quoted","bytes_hex":"613d6200"},"occurrence":0}]"#
        );
    }

    #[test]
    fn resolver_output_is_display_only_and_does_not_merge_raw_keys() {
        let mut resolver = HashMap::new();
        resolver.insert(0x1001, "same");
        resolver.insert(0x1002, "same");
        let tokens = [
            Token::Id(0x1001),
            Token::Equal,
            Token::U32(1),
            Token::Id(0x1002),
            Token::Equal,
            Token::U32(2),
            Token::Id(0x1001),
            Token::Equal,
            Token::U32(3),
        ];
        let (source, _) = encode(&tokens);
        let document = walk_binary_with_resolver(&source, Some(&resolver))
            .expect("resolved keys should parse");

        assert_eq!(
            document.events[0].key.as_ref().unwrap().resolved.as_deref(),
            Some("same")
        );
        assert_eq!(
            document.events[1].key.as_ref().unwrap().resolved.as_deref(),
            Some("same")
        );
        assert_eq!(
            document.events[0].path,
            vec![field_segment(RawTokenIdentity::Id { token: 0x1001 }, 0)]
        );
        assert_eq!(
            document.events[1].path,
            vec![field_segment(RawTokenIdentity::Id { token: 0x1002 }, 0)]
        );
        assert_eq!(
            document.events[2].path,
            vec![field_segment(RawTokenIdentity::Id { token: 0x1001 }, 1)]
        );
        assert!(
            !document.events[0]
                .canonical_path_json()
                .unwrap()
                .contains("same")
        );
    }

    #[test]
    fn identifier_and_lookup_values_preserve_display_resolution() {
        struct Resolver;

        impl TokenResolver for Resolver {
            fn resolve(&self, token: u16) -> Option<&str> {
                (token == 0x2000).then_some("resolved_identifier_value")
            }

            fn lookup(&self, index: u32) -> Option<&str> {
                (index == 7).then_some("resolved_lookup_value")
            }
        }

        let (source, _) = encode(&[
            Token::Id(0x1000),
            Token::Equal,
            Token::Id(0x2000),
            Token::Id(0x1001),
            Token::Equal,
            Token::Lookup(7),
        ]);
        let document = walk_binary_with_resolver(&source, Some(&Resolver)).unwrap();

        assert_eq!(
            document.events[0].value,
            StructuralValue::Scalar {
                raw: RawTokenIdentity::Id { token: 0x2000 },
                resolved: Some("resolved_identifier_value".to_owned()),
            }
        );
        assert_eq!(
            document.events[1].value,
            StructuralValue::Scalar {
                raw: RawTokenIdentity::Lookup { index: 7 },
                resolved: Some("resolved_lookup_value".to_owned()),
            }
        );
        assert!(
            !document.events[0]
                .canonical_path_json()
                .unwrap()
                .contains("resolved")
        );
    }

    #[test]
    fn malformed_structures_are_rejected_with_precise_offsets() {
        let (equal_only, equal_spans) = encode(&[Token::Equal]);
        assert_eq!(
            walk_binary(&equal_only).unwrap_err(),
            StructuralError {
                offset: equal_spans[0].start,
                kind: StructuralErrorKind::EqualWithoutKey,
            }
        );

        let (repeated, repeated_spans) = encode(&[Token::Id(0x1001), Token::Equal, Token::Equal]);
        assert_eq!(
            walk_binary(&repeated).unwrap_err(),
            StructuralError {
                offset: repeated_spans[2].start,
                kind: StructuralErrorKind::RepeatedEqual,
            }
        );

        let (unmatched, unmatched_spans) = encode(&[Token::Close]);
        assert_eq!(
            walk_binary(&unmatched).unwrap_err(),
            StructuralError {
                offset: unmatched_spans[0].start,
                kind: StructuralErrorKind::UnexpectedClose,
            }
        );

        let (missing, _) = encode(&[Token::Id(0x1001), Token::Equal]);
        assert_eq!(
            walk_binary(&missing).unwrap_err(),
            StructuralError {
                offset: missing.len(),
                kind: StructuralErrorKind::FieldMissingValue,
            }
        );

        let (missing_before_close, close_spans) =
            encode(&[Token::Open, Token::Id(0x1001), Token::Equal, Token::Close]);
        assert_eq!(
            walk_binary(&missing_before_close).unwrap_err(),
            StructuralError {
                offset: close_spans[3].start,
                kind: StructuralErrorKind::FieldMissingValue,
            }
        );

        let (unclosed, _) = encode(&[Token::Open, Token::Open, Token::Close]);
        assert_eq!(
            walk_binary(&unclosed).unwrap_err(),
            StructuralError {
                offset: unclosed.len(),
                kind: StructuralErrorKind::UnclosedContainers { count: 1 },
            }
        );

        let (mut truncated, _) = encode(&[Token::Quoted(Scalar::new(b"abc"))]);
        truncated.pop();
        let error = walk_binary(&truncated).unwrap_err();
        assert!(matches!(error.kind, StructuralErrorKind::Reader { .. }));
    }

    #[test]
    fn token_write_roundtrip_preserves_generated_source_and_event_spans() {
        let tokens = [
            Token::Id(0x1001),
            Token::Equal,
            Token::Open,
            Token::Quoted(Scalar::new(b"key")),
            Token::Equal,
            Token::Rgb(Rgb {
                r: 1,
                g: 2,
                b: 3,
                a: None,
            }),
            Token::Lookup(255),
            Token::Close,
        ];
        let (source, spans) = encode(&tokens);
        let document = walk_binary(&source).expect("roundtrip fixture should parse");

        let mut reader = TokenReader::from_slice(&source);
        let mut rewritten = Vec::new();
        while let Some(token) = reader.next().expect("source token should decode") {
            token.write(&mut rewritten).expect("token should re-encode");
        }
        assert_eq!(rewritten, source);

        assert_eq!(document.source_len, source.len());
        assert_eq!(document.events[0].key_span, Some(spans[0]));
        assert_eq!(document.events[0].value_span.start, spans[2].start);
        assert_eq!(document.events[0].value_span.end, spans[7].end);
        assert_eq!(document.events[1].key_span, Some(spans[3]));
        assert_eq!(document.events[1].value_span, spans[5]);
        assert_eq!(document.events[2].value_span, spans[6]);
    }
}
