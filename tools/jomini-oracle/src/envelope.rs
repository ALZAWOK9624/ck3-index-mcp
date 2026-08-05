//! Layout-aware, bounded access to the writable CK3 binary save envelopes.
//!
//! Three canonical layouts are recognized, and each one pins where the two
//! logical sections physically live:
//!
//! | layout                 | metadata              | gamestate               |
//! |------------------------|-----------------------|-------------------------|
//! | `binary_uncompressed`  | inline after header   | inline after metadata   |
//! | `unified_binary_zip`   | inline after header   | ZIP entry `gamestate`   |
//! | `split_binary_zip`     | ZIP entry `meta`      | ZIP entry `gamestate`   |
//!
//! A header that disagrees with the container it was found in is rejected
//! rather than reinterpreted, so a save can never be edited through a layout it
//! does not actually have. Text headers are refused outright: every layout here
//! is binary, which is what makes the single header-kind check sufficient to
//! keep text sections out of the writing paths.

use std::{collections::BTreeMap, fmt, io::Read, ops::Range};

use jomini::envelope::{JominiFile, JominiFileKind, SaveHeader, SaveHeaderKind};

use crate::zip_rebuild::{
    ZipEntryManifest, ZipRebuildError, ZipRebuildLimits, rebuild_zip_detailed,
};

/// The two logical sections of a save.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SaveSection {
    Metadata,
    Gamestate,
}

impl SaveSection {
    pub const fn name(self) -> &'static str {
        match self {
            Self::Metadata => "metadata",
            Self::Gamestate => "gamestate",
        }
    }

    pub const fn other(self) -> Self {
        match self {
            Self::Metadata => Self::Gamestate,
            Self::Gamestate => Self::Metadata,
        }
    }
}

/// A canonical, writable binary save layout.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SaveLayout {
    /// Header kind 1: both sections are plain bytes, no archive at all.
    BinaryUncompressed,
    /// Header kind 3: inline metadata immediately followed by the archive.
    UnifiedBinaryZip,
    /// Header kind 5: the archive holds both `meta` and `gamestate`.
    SplitBinaryZip,
}

impl SaveLayout {
    pub const fn name(self) -> &'static str {
        match self {
            Self::BinaryUncompressed => "binary_uncompressed",
            Self::UnifiedBinaryZip => "unified_binary_zip",
            Self::SplitBinaryZip => "split_binary_zip",
        }
    }

    pub const fn container(self) -> &'static str {
        match self {
            Self::BinaryUncompressed => "uncompressed",
            Self::UnifiedBinaryZip | Self::SplitBinaryZip => "zip",
        }
    }

    /// Where a section physically lives in this layout.
    pub const fn storage(self, section: SaveSection) -> SectionStorage {
        match (self, section) {
            (Self::BinaryUncompressed, SaveSection::Metadata)
            | (Self::UnifiedBinaryZip, SaveSection::Metadata) => SectionStorage::InlineMetadata,
            (Self::BinaryUncompressed, SaveSection::Gamestate) => SectionStorage::InlineGamestate,
            (Self::UnifiedBinaryZip, SaveSection::Gamestate)
            | (Self::SplitBinaryZip, SaveSection::Gamestate) => {
                SectionStorage::ZipEntry(GAMESTATE_ENTRY)
            }
            (Self::SplitBinaryZip, SaveSection::Metadata) => SectionStorage::ZipEntry(META_ENTRY),
        }
    }
}

pub const GAMESTATE_ENTRY: &str = "gamestate";
pub const META_ENTRY: &str = "meta";

/// The physical home of one logical section.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SectionStorage {
    InlineMetadata,
    InlineGamestate,
    ZipEntry(&'static str),
}

impl SectionStorage {
    pub const fn name(self) -> &'static str {
        match self {
            Self::InlineMetadata => "inline_after_header",
            Self::InlineGamestate => "inline_after_metadata",
            Self::ZipEntry(_) => "zip_entry",
        }
    }

    pub const fn zip_entry(self) -> Option<&'static str> {
        match self {
            Self::ZipEntry(name) => Some(name),
            _ => None,
        }
    }
}

/// Resource ceilings applied while reading and rebuilding a save.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct EnvelopeLimits {
    pub max_section_bytes: u64,
    pub zip: ZipRebuildLimits,
}

/// A refusal while analyzing, reading, or rebuilding a save envelope.
#[derive(Debug)]
pub enum EnvelopeError {
    Parse(String),
    /// The header kind is not one of the three writable binary layouts.
    UnsupportedHeaderKind {
        kind_code: u16,
    },
    /// The header kind and the container actually present disagree.
    ContainerMismatch {
        layout: &'static str,
        detail: &'static str,
    },
    /// A declared offset or length does not fit inside the save.
    Bounds(String),
    /// The archive manifest is unsupported, damaged, or ambiguous.
    Zip(ZipRebuildError),
    /// A section exceeded its byte ceiling.
    SectionTooLarge {
        section: &'static str,
        limit: u64,
    },
    /// A self-check after rebuilding did not reproduce the intended bytes.
    SelfCheck(String),
}

impl fmt::Display for EnvelopeError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Parse(detail) => write!(f, "cannot read save envelope: {detail}"),
            Self::UnsupportedHeaderKind { kind_code } => write!(
                f,
                "unsupported save header kind 0x{kind_code:02x}; only binary, unified_binary, \
                 and split_binary are writable"
            ),
            Self::ContainerMismatch { layout, detail } => {
                write!(f, "{layout} header does not match its container: {detail}")
            }
            Self::Bounds(detail) => write!(f, "save envelope bounds are inconsistent: {detail}"),
            Self::Zip(source) => write!(f, "unsupported or invalid embedded ZIP: {source}"),
            Self::SectionTooLarge { section, limit } => {
                write!(f, "{section} exceeds the {limit}-byte edit limit")
            }
            Self::SelfCheck(detail) => write!(f, "envelope self-check failed: {detail}"),
        }
    }
}

impl std::error::Error for EnvelopeError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Zip(source) => Some(source),
            _ => None,
        }
    }
}

/// The validated shape of one save file.
#[derive(Debug, Clone)]
pub struct SaveEnvelope {
    header: SaveHeader,
    layout: SaveLayout,
    inline_metadata: Option<Range<usize>>,
    inline_gamestate: Option<Range<usize>>,
    zip_start: Option<usize>,
    zip_entries: Vec<ZipEntryManifest>,
}

impl SaveEnvelope {
    pub fn header(&self) -> &SaveHeader {
        &self.header
    }

    pub fn layout(&self) -> SaveLayout {
        self.layout
    }

    /// The archive's byte offset, for layouts that have one.
    pub fn zip_start(&self) -> Option<usize> {
        self.zip_start
    }

    /// Source archive entries in central-directory order.
    pub fn zip_entries(&self) -> &[ZipEntryManifest] {
        &self.zip_entries
    }

    /// The inline byte range of a section, when it is stored inline.
    pub fn inline_span(&self, section: SaveSection) -> Option<Range<usize>> {
        match section {
            SaveSection::Metadata => self.inline_metadata.clone(),
            SaveSection::Gamestate => self.inline_gamestate.clone(),
        }
    }

    pub fn storage(&self, section: SaveSection) -> SectionStorage {
        self.layout.storage(section)
    }

    /// The header bytes that must survive any edit unchanged.
    ///
    /// The eight-byte metadata-length field at `[15, 23)` is deliberately
    /// excluded: it is the only header field an inline-metadata edit rewrites.
    pub fn header_immutable_spans(&self) -> [Range<usize>; 2] {
        [
            0..METADATA_LEN_FIELD.start,
            METADATA_LEN_FIELD.end..self.header.header_len(),
        ]
    }
}

const METADATA_LEN_FIELD: Range<usize> = 15..23;
const ZIP_LOCAL_SIGNATURE: &[u8; 4] = b"PK\x03\x04";

/// Validates one save file and resolves its canonical layout.
pub fn analyze(data: &[u8], limits: EnvelopeLimits) -> Result<SaveEnvelope, EnvelopeError> {
    let save =
        JominiFile::from_slice(data).map_err(|error| EnvelopeError::Parse(error.to_string()))?;
    let header = save.header().clone();
    let layout = match header.kind() {
        SaveHeaderKind::Binary => SaveLayout::BinaryUncompressed,
        SaveHeaderKind::UnifiedBinary => SaveLayout::UnifiedBinaryZip,
        SaveHeaderKind::SplitBinary => SaveLayout::SplitBinaryZip,
        other => {
            return Err(EnvelopeError::UnsupportedHeaderKind {
                kind_code: other.value(),
            });
        }
    };

    let header_len = header.header_len();
    if header_len > data.len() {
        return Err(EnvelopeError::Bounds(
            "save is shorter than its declared header".to_owned(),
        ));
    }
    let declared_metadata = usize::try_from(header.metadata_len())
        .map_err(|_| EnvelopeError::Bounds("declared metadata length does not fit".to_owned()))?;

    let has_zip = matches!(save.kind(), JominiFileKind::Zip(_));
    match (layout, has_zip) {
        (SaveLayout::BinaryUncompressed, true) => {
            return Err(EnvelopeError::ContainerMismatch {
                layout: "binary",
                detail: "an embedded ZIP was found where plain inline sections were declared",
            });
        }
        (SaveLayout::UnifiedBinaryZip, false) | (SaveLayout::SplitBinaryZip, false) => {
            return Err(EnvelopeError::ContainerMismatch {
                layout: "unified_binary/split_binary",
                detail: "no valid embedded ZIP container was found",
            });
        }
        _ => {}
    }

    let (inline_metadata, inline_gamestate, zip_start) = match layout {
        SaveLayout::BinaryUncompressed => {
            let metadata_end = inline_metadata_end(data, header_len, declared_metadata)?;
            if metadata_end == data.len() {
                return Err(EnvelopeError::Bounds(
                    "declared metadata consumes the whole save, leaving no gamestate".to_owned(),
                ));
            }
            (
                Some(header_len..metadata_end),
                Some(metadata_end..data.len()),
                None,
            )
        }
        SaveLayout::UnifiedBinaryZip => {
            let metadata_end = inline_metadata_end(data, header_len, declared_metadata)?;
            if !data[metadata_end..].starts_with(ZIP_LOCAL_SIGNATURE) {
                return Err(EnvelopeError::ContainerMismatch {
                    layout: "unified_binary",
                    detail: "the embedded ZIP does not begin immediately after inline metadata",
                });
            }
            (Some(header_len..metadata_end), None, Some(metadata_end))
        }
        SaveLayout::SplitBinaryZip => {
            if declared_metadata != 0 {
                return Err(EnvelopeError::ContainerMismatch {
                    layout: "split_binary",
                    detail: "the header declares inline metadata, but metadata belongs to the ZIP",
                });
            }
            if !data[header_len..].starts_with(ZIP_LOCAL_SIGNATURE) {
                return Err(EnvelopeError::ContainerMismatch {
                    layout: "split_binary",
                    detail: "the embedded ZIP does not begin immediately after the header",
                });
            }
            (None, None, Some(header_len))
        }
    };

    let zip_entries = match zip_start {
        None => Vec::new(),
        Some(start) => {
            rebuild_zip_detailed(&data[start..], &BTreeMap::new(), limits.zip)
                .map_err(EnvelopeError::Zip)?
                .source_entries
        }
    };

    let has_entry = |name: &str| {
        zip_entries
            .iter()
            .any(|entry| entry.name.as_slice() == name.as_bytes())
    };
    match layout {
        SaveLayout::UnifiedBinaryZip if has_entry(META_ENTRY) => {
            return Err(EnvelopeError::ContainerMismatch {
                layout: "unified_binary",
                detail: "the ZIP carries a meta entry that would compete with inline metadata",
            });
        }
        SaveLayout::SplitBinaryZip if !has_entry(META_ENTRY) => {
            return Err(EnvelopeError::ContainerMismatch {
                layout: "split_binary",
                detail: "the ZIP has no meta entry to hold the declared split metadata",
            });
        }
        _ => {}
    }
    if zip_start.is_some() && !has_entry(GAMESTATE_ENTRY) {
        return Err(EnvelopeError::ContainerMismatch {
            layout: "zip",
            detail: "the ZIP has no gamestate entry",
        });
    }

    Ok(SaveEnvelope {
        header,
        layout,
        inline_metadata,
        inline_gamestate,
        zip_start,
        zip_entries,
    })
}

fn inline_metadata_end(
    data: &[u8],
    header_len: usize,
    declared_metadata: usize,
) -> Result<usize, EnvelopeError> {
    header_len
        .checked_add(declared_metadata)
        .filter(|end| *end <= data.len())
        .ok_or_else(|| {
            EnvelopeError::Bounds("declared metadata extends beyond the save".to_owned())
        })
}

/// One section's decoded bytes plus how strongly they were verified.
#[derive(Debug, Clone)]
pub struct SectionBytes {
    pub bytes: Vec<u8>,
    /// True when a CRC32 and declared-size verifier consumed the section.
    pub integrity_checked: bool,
}

/// Reads one complete section, refusing oversized sections.
pub fn read_section(
    data: &[u8],
    envelope: &SaveEnvelope,
    section: SaveSection,
    limits: EnvelopeLimits,
) -> Result<SectionBytes, EnvelopeError> {
    match envelope.storage(section) {
        SectionStorage::InlineMetadata | SectionStorage::InlineGamestate => {
            let span = envelope.inline_span(section).ok_or_else(|| {
                EnvelopeError::Bounds("inline section span is missing".to_owned())
            })?;
            let bytes = data
                .get(span)
                .ok_or_else(|| {
                    EnvelopeError::Bounds("inline section span lies outside the save".to_owned())
                })?
                .to_vec();
            enforce_section_limit(section, bytes.len() as u64, limits)?;
            Ok(SectionBytes {
                bytes,
                integrity_checked: false,
            })
        }
        SectionStorage::ZipEntry(entry) => read_zip_entry(data, envelope, section, entry, limits),
    }
}

fn read_zip_entry(
    data: &[u8],
    envelope: &SaveEnvelope,
    section: SaveSection,
    entry: &'static str,
    limits: EnvelopeLimits,
) -> Result<SectionBytes, EnvelopeError> {
    let declared = envelope
        .zip_entries
        .iter()
        .find(|candidate| candidate.name.as_slice() == entry.as_bytes())
        .ok_or(EnvelopeError::ContainerMismatch {
            layout: "zip",
            detail: "the declared section entry is missing from the manifest",
        })?;
    enforce_section_limit(section, declared.uncompressed_bytes, limits)?;

    let save =
        JominiFile::from_slice(data).map_err(|error| EnvelopeError::Parse(error.to_string()))?;
    let JominiFileKind::Zip(zip) = save.kind() else {
        return Err(EnvelopeError::ContainerMismatch {
            layout: "zip",
            detail: "the container stopped presenting itself as a ZIP",
        });
    };

    // The verifying reader only settles its CRC32 and declared-size claims once
    // it has been read to EOF, so the bounded read below is what verifies it.
    let mut reader = zip
        .read_entry_verified(entry)
        .map_err(|error| EnvelopeError::Parse(error.to_string()))?;
    let mut bytes = Vec::with_capacity(usize::try_from(declared.uncompressed_bytes).unwrap_or(0));
    (&mut reader)
        .take(limits.max_section_bytes + 1)
        .read_to_end(&mut bytes)
        .map_err(|error| {
            EnvelopeError::Parse(format!("cannot fully read and verify {entry}: {error}"))
        })?;
    enforce_section_limit(section, bytes.len() as u64, limits)?;
    if bytes.len() as u64 != declared.uncompressed_bytes {
        return Err(EnvelopeError::SelfCheck(format!(
            "{entry} produced {} bytes but the archive declared {}",
            bytes.len(),
            declared.uncompressed_bytes
        )));
    }
    Ok(SectionBytes {
        bytes,
        integrity_checked: true,
    })
}

fn enforce_section_limit(
    section: SaveSection,
    actual: u64,
    limits: EnvelopeLimits,
) -> Result<(), EnvelopeError> {
    if actual > limits.max_section_bytes {
        return Err(EnvelopeError::SectionTooLarge {
            section: section.name(),
            limit: limits.max_section_bytes,
        });
    }
    Ok(())
}

/// How one section's replacement is written back into a save.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RebuildStrategy {
    /// Splice inline metadata and rewrite the header length field when needed.
    SpliceInlineMetadata,
    /// Splice the inline gamestate tail; the header is untouched.
    SpliceInlineGamestate,
    /// Rebuild the logical ZIP, replacing exactly one entry.
    RebuildZipEntry(&'static str),
}

impl RebuildStrategy {
    pub const fn name(self) -> &'static str {
        match self {
            Self::SpliceInlineMetadata => "splice_inline_metadata",
            Self::SpliceInlineGamestate => "splice_inline_gamestate",
            Self::RebuildZipEntry(_) => "rebuild_zip_entry",
        }
    }

    pub const fn zip_entry(self) -> Option<&'static str> {
        match self {
            Self::RebuildZipEntry(name) => Some(name),
            _ => None,
        }
    }
}

/// The strategy this layout uses to write one section back.
pub const fn rebuild_strategy(layout: SaveLayout, section: SaveSection) -> RebuildStrategy {
    match layout.storage(section) {
        SectionStorage::InlineMetadata => RebuildStrategy::SpliceInlineMetadata,
        SectionStorage::InlineGamestate => RebuildStrategy::SpliceInlineGamestate,
        SectionStorage::ZipEntry(name) => RebuildStrategy::RebuildZipEntry(name),
    }
}

/// Produces a new save carrying `edited_section` in place of `section`.
///
/// The source is never modified. The candidate is re-analyzed, both sections
/// are read back, and every byte region the strategy promised not to touch is
/// compared literally before the bytes are returned.
pub fn rebuild_save(
    source: &[u8],
    envelope: &SaveEnvelope,
    section: SaveSection,
    edited_section: &[u8],
    limits: EnvelopeLimits,
) -> Result<Vec<u8>, EnvelopeError> {
    enforce_section_limit(section, edited_section.len() as u64, limits)?;
    let strategy = rebuild_strategy(envelope.layout, section);
    let candidate = match strategy {
        RebuildStrategy::SpliceInlineMetadata => {
            splice_inline_metadata(source, envelope, edited_section)?
        }
        RebuildStrategy::SpliceInlineGamestate => {
            splice_inline_gamestate(source, envelope, edited_section)?
        }
        RebuildStrategy::RebuildZipEntry(entry) => {
            rebuild_zip_entry(source, envelope, entry, edited_section, limits)?
        }
    };

    require_untouched_bytes(source, &candidate, envelope, strategy, edited_section.len())?;

    let rebuilt = analyze(&candidate, limits)?;
    if rebuilt.layout != envelope.layout {
        return Err(EnvelopeError::SelfCheck(
            "the rebuilt save changed its layout".to_owned(),
        ));
    }
    if rebuilt.header.header_len() != envelope.header.header_len()
        || rebuilt.header.version() != envelope.header.version()
        || rebuilt.header.kind() != envelope.header.kind()
    {
        return Err(EnvelopeError::SelfCheck(
            "the rebuilt save changed header fields outside the metadata length".to_owned(),
        ));
    }
    let expected_metadata_len = if strategy == RebuildStrategy::SpliceInlineMetadata {
        edited_section.len() as u64
    } else {
        envelope.header.metadata_len()
    };
    if rebuilt.header.metadata_len() != expected_metadata_len {
        return Err(EnvelopeError::SelfCheck(
            "the rebuilt save declares an unexpected metadata length".to_owned(),
        ));
    }

    let readback = read_section(&candidate, &rebuilt, section, limits)?;
    if readback.bytes != edited_section {
        return Err(EnvelopeError::SelfCheck(format!(
            "the rebuilt {} differs from the intended bytes",
            section.name()
        )));
    }
    let rebuilt_other = read_section(&candidate, &rebuilt, section.other(), limits)?;
    let source_other = read_section(source, envelope, section.other(), limits)?;
    if rebuilt_other.bytes != source_other.bytes {
        return Err(EnvelopeError::SelfCheck(format!(
            "the rebuilt save changed its {}",
            section.other().name()
        )));
    }
    Ok(candidate)
}

/// Compares the byte regions the chosen strategy is not allowed to change.
fn require_untouched_bytes(
    source: &[u8],
    candidate: &[u8],
    envelope: &SaveEnvelope,
    strategy: RebuildStrategy,
    edited_len: usize,
) -> Result<(), EnvelopeError> {
    for span in envelope.header_immutable_spans() {
        if source.get(span.clone()) != candidate.get(span) {
            return Err(EnvelopeError::SelfCheck(
                "the rebuilt save changed immutable header bytes".to_owned(),
            ));
        }
    }
    match strategy {
        RebuildStrategy::SpliceInlineMetadata => {
            let span = envelope.inline_span(SaveSection::Metadata).ok_or_else(|| {
                EnvelopeError::Bounds("inline metadata span is missing".to_owned())
            })?;
            let candidate_tail_start = span
                .start
                .checked_add(edited_len)
                .ok_or_else(|| EnvelopeError::Bounds("edited metadata end overflows".to_owned()))?;
            if source.get(span.end..) != candidate.get(candidate_tail_start..) {
                return Err(EnvelopeError::SelfCheck(
                    "the metadata edit changed bytes after the inline metadata".to_owned(),
                ));
            }
        }
        RebuildStrategy::SpliceInlineGamestate => {
            let span = envelope
                .inline_span(SaveSection::Gamestate)
                .ok_or_else(|| {
                    EnvelopeError::Bounds("inline gamestate span is missing".to_owned())
                })?;
            if source.get(..span.start) != candidate.get(..span.start) {
                return Err(EnvelopeError::SelfCheck(
                    "the gamestate edit changed the header or inline metadata".to_owned(),
                ));
            }
        }
        RebuildStrategy::RebuildZipEntry(_) => {
            let zip_start = envelope.zip_start.ok_or_else(|| {
                EnvelopeError::Bounds("this layout has no embedded ZIP".to_owned())
            })?;
            if source.get(..zip_start) != candidate.get(..zip_start) {
                return Err(EnvelopeError::SelfCheck(
                    "the ZIP rebuild changed bytes outside the logical ZIP".to_owned(),
                ));
            }
        }
    }
    Ok(())
}

fn splice_inline_metadata(
    source: &[u8],
    envelope: &SaveEnvelope,
    edited_metadata: &[u8],
) -> Result<Vec<u8>, EnvelopeError> {
    let span = envelope
        .inline_span(SaveSection::Metadata)
        .ok_or_else(|| EnvelopeError::Bounds("inline metadata span is missing".to_owned()))?;
    let header = rewrite_metadata_length_field(source, &envelope.header, edited_metadata.len())?;
    if header.len() != span.start {
        return Err(EnvelopeError::SelfCheck(
            "the rewritten header changed its physical width".to_owned(),
        ));
    }
    let mut edited = Vec::with_capacity(source.len() - span.len() + edited_metadata.len());
    edited.extend_from_slice(&header);
    edited.extend_from_slice(edited_metadata);
    edited.extend_from_slice(&source[span.end..]);
    Ok(edited)
}

fn splice_inline_gamestate(
    source: &[u8],
    envelope: &SaveEnvelope,
    edited_gamestate: &[u8],
) -> Result<Vec<u8>, EnvelopeError> {
    let span = envelope
        .inline_span(SaveSection::Gamestate)
        .ok_or_else(|| EnvelopeError::Bounds("inline gamestate span is missing".to_owned()))?;
    if span.end != source.len() {
        return Err(EnvelopeError::Bounds(
            "the inline gamestate does not extend to the end of the save".to_owned(),
        ));
    }
    let mut edited = Vec::with_capacity(span.start + edited_gamestate.len());
    edited.extend_from_slice(&source[..span.start]);
    edited.extend_from_slice(edited_gamestate);
    Ok(edited)
}

fn rebuild_zip_entry(
    source: &[u8],
    envelope: &SaveEnvelope,
    entry: &'static str,
    edited_section: &[u8],
    limits: EnvelopeLimits,
) -> Result<Vec<u8>, EnvelopeError> {
    let zip_start = envelope
        .zip_start
        .ok_or_else(|| EnvelopeError::Bounds("this layout has no embedded ZIP".to_owned()))?;
    let mut replacements = BTreeMap::new();
    replacements.insert(entry.as_bytes().to_vec(), edited_section.to_vec());
    let rebuilt = rebuild_zip_detailed(&source[zip_start..], &replacements, limits.zip)
        .map_err(EnvelopeError::Zip)?;
    let mut edited = Vec::with_capacity(zip_start + rebuilt.bytes.len());
    edited.extend_from_slice(&source[..zip_start]);
    edited.extend_from_slice(&rebuilt.bytes);
    Ok(edited)
}

/// Rewrites only the header's eight-byte metadata-length field.
fn rewrite_metadata_length_field(
    source: &[u8],
    header: &SaveHeader,
    metadata_len: usize,
) -> Result<Vec<u8>, EnvelopeError> {
    let metadata_len = u32::try_from(metadata_len).map_err(|_| {
        EnvelopeError::Bounds("edited metadata exceeds the header's 32-bit length field".to_owned())
    })?;
    let header_len = header.header_len();
    let mut rewritten = source
        .get(..header_len)
        .ok_or_else(|| EnvelopeError::Bounds("save is shorter than its header".to_owned()))?
        .to_vec();

    if header.metadata_len() != u64::from(metadata_len) {
        let field = rewritten.get_mut(METADATA_LEN_FIELD).ok_or_else(|| {
            EnvelopeError::Bounds("save header has no metadata-length field".to_owned())
        })?;
        field.copy_from_slice(format!("{metadata_len:08x}").as_bytes());
    }

    let checked = SaveHeader::from_slice(&rewritten)
        .map_err(|error| EnvelopeError::Parse(error.to_string()))?;
    if checked.version() != header.version()
        || checked.kind() != header.kind()
        || checked.header_len() != header_len
        || checked.metadata_len() != u64::from(metadata_len)
    {
        return Err(EnvelopeError::SelfCheck(
            "the rewritten header changed fields outside the metadata length".to_owned(),
        ));
    }
    Ok(rewritten)
}
