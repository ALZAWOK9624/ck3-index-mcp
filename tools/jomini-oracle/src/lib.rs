//! Reusable, resource-bounded CK3/Jomini inspection primitives.

#![forbid(unsafe_code)]

pub mod envelope;
pub mod sha256;
pub mod structural;
pub mod zip_rebuild;

pub use envelope::{
    EnvelopeError, EnvelopeLimits, RebuildStrategy, SaveEnvelope, SaveLayout, SaveSection,
    SectionBytes, SectionStorage,
};

pub use sha256::{
    MAX_MESSAGE_BYTES, MessageTooLong, Sha256, lowercase_hex, sha256_bytes, sha256_reader,
};

pub use structural::{
    ByteSpan, KeyView, PathSegment, RawTokenIdentity, StructuralBudget, StructuralDocument,
    StructuralError, StructuralErrorKind, StructuralEvent, StructuralEventKind, StructuralResource,
    StructuralValue, TextRepresentation, walk_binary, walk_binary_with_budget,
    walk_binary_with_resolver, walk_binary_with_resolver_and_budget,
};
