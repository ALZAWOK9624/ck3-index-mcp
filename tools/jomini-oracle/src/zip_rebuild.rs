//! Bounded, copy-on-write rebuilding for the logical ZIP portion of CK3 saves.
//!
//! The caller must pass the ZIP bytes without the CK3 envelope/prefix.  Output
//! offsets consequently start at zero and remain relative to the ZIP start when
//! the caller later appends the returned bytes to a CK3 prefix.

use std::collections::{BTreeMap, BTreeSet};
use std::fmt;
use std::io::{self, Read, Write};

use flate2::read::DeflateDecoder;
use flate2::write::DeflateEncoder;
use rawzip::path::EntryPath;
use rawzip::{CompressionMethod, Crc32, DataDescriptorOutput, ZipArchive, ZipArchiveWriter};

const FLAG_DATA_DESCRIPTOR: u16 = 1 << 3;
const LOCAL_HEADER_BYTES: u64 = 30;
const CENTRAL_HEADER_BYTES: usize = 46;
const EOCD_HEADER_BYTES: usize = 22;
const DATA_DESCRIPTOR_SIGNATURE: u32 = 0x0807_4b50;

/// Resource ceilings applied to both the source archive and rebuilt archive.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct ZipRebuildLimits {
    pub max_entries: usize,
    pub max_total_compressed_bytes: u64,
    pub max_total_uncompressed_bytes: u64,
}

impl Default for ZipRebuildLimits {
    fn default() -> Self {
        Self {
            max_entries: 1_024,
            max_total_compressed_bytes: 256 * 1024 * 1024,
            max_total_uncompressed_bytes: 512 * 1024 * 1024,
        }
    }
}

/// A refusal or integrity failure while rebuilding a ZIP archive.
#[derive(Debug)]
pub enum ZipRebuildError {
    Zip {
        context: &'static str,
        source: rawzip::Error,
    },
    Io {
        context: &'static str,
        source: io::Error,
    },
    EntryCountMismatch {
        declared: u64,
        actual: usize,
    },
    LimitExceeded {
        resource: &'static str,
        limit: u64,
        actual: u64,
    },
    OutputLimitExceeded {
        resource: &'static str,
        limit: u64,
        attempted: u64,
    },
    DuplicateName(Vec<u8>),
    MissingReplacement(Vec<u8>),
    DirectoryEntry(Vec<u8>),
    EncryptedEntry(Vec<u8>),
    EntryFlagsMismatch {
        name: Vec<u8>,
        central: u16,
        local: u16,
    },
    UnsupportedEntryFlags {
        name: Vec<u8>,
        flags: u16,
    },
    UnsupportedEntryMetadata {
        name: Vec<u8>,
        field: &'static str,
    },
    LocalCentralHeaderMismatch {
        name: Vec<u8>,
        field: &'static str,
        local: u64,
        central: u64,
    },
    MultiDiskArchive,
    ArchivePrelude(u64),
    TrailingData {
        archive_end: u64,
        source_len: u64,
    },
    UnsupportedCompression {
        name: Vec<u8>,
        method: u16,
    },
    OverlappingEntries,
    ArchiveLayout(String),
    LocalCentralNameMismatch(Vec<u8>),
    Integrity {
        phase: &'static str,
        name: Vec<u8>,
        detail: String,
    },
    VerificationMismatch {
        name: Vec<u8>,
        detail: &'static str,
    },
}

/// Renders an entry name for diagnostics.
///
/// Entry identity is what distinguishes `meta` from `gamestate`, so a refusal
/// has to say which entry it is about in readable form rather than as a byte
/// array. Invalid UTF-8 is replaced rather than hidden.
fn display_name(name: &[u8]) -> impl fmt::Debug + '_ {
    struct EntryName<'a>(&'a [u8]);

    impl fmt::Debug for EntryName<'_> {
        fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
            fmt::Debug::fmt(&String::from_utf8_lossy(self.0), f)
        }
    }

    EntryName(name)
}

impl fmt::Display for ZipRebuildError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Zip { context, source } => write!(f, "{context}: {source}"),
            Self::Io { context, source } => write!(f, "{context}: {source}"),
            Self::EntryCountMismatch { declared, actual } => write!(
                f,
                "EOCD declares {declared} entries, but the central directory contains {actual}"
            ),
            Self::LimitExceeded {
                resource,
                limit,
                actual,
            } => write!(f, "{resource} limit exceeded: {actual} > {limit}"),
            Self::OutputLimitExceeded {
                resource,
                limit,
                attempted,
            } => write!(
                f,
                "{resource} write would exceed its limit: {attempted} > {limit}"
            ),
            Self::DuplicateName(name) => {
                write!(f, "duplicate ZIP entry name: {:?}", display_name(name))
            }
            Self::MissingReplacement(name) => {
                write!(
                    f,
                    "replacement target does not exist: {:?}",
                    display_name(name)
                )
            }
            Self::DirectoryEntry(name) => {
                write!(
                    f,
                    "directory ZIP entry is not supported: {:?}",
                    display_name(name)
                )
            }
            Self::EncryptedEntry(name) => {
                write!(
                    f,
                    "encrypted ZIP entry is not supported: {:?}",
                    display_name(name)
                )
            }
            Self::EntryFlagsMismatch {
                name,
                central,
                local,
            } => write!(
                f,
                "local and central ZIP flags disagree for {:?}: {local:#06x} != {central:#06x}",
                display_name(name)
            ),
            Self::UnsupportedEntryFlags { name, flags } => write!(
                f,
                "unsupported ZIP entry flags {flags:#06x} for {:?}; only the data-descriptor bit is accepted",
                display_name(name)
            ),
            Self::UnsupportedEntryMetadata { name, field } => {
                write!(
                    f,
                    "unsupported ZIP entry metadata {field} for {:?}",
                    display_name(name)
                )
            }
            Self::LocalCentralHeaderMismatch {
                name,
                field,
                local,
                central,
            } => write!(
                f,
                "local and central {field} disagree for {:?}: {local} != {central}",
                display_name(name)
            ),
            Self::MultiDiskArchive => write!(f, "multi-disk ZIP archives are not supported"),
            Self::ArchivePrelude(offset) => write!(
                f,
                "logical ZIP input contains a {offset}-byte prelude; slice the CK3 prefix first"
            ),
            Self::TrailingData {
                archive_end,
                source_len,
            } => write!(
                f,
                "ZIP ends at byte {archive_end}, but input contains {source_len} bytes"
            ),
            Self::UnsupportedCompression { name, method } => write!(
                f,
                "unsupported compression method {method} for entry {:?}",
                display_name(name)
            ),
            Self::OverlappingEntries => write!(f, "ZIP records overlap"),
            Self::ArchiveLayout(detail) => write!(f, "unsupported ZIP record layout: {detail}"),
            Self::LocalCentralNameMismatch(name) => write!(
                f,
                "local and central directory names disagree for entry {:?}",
                display_name(name)
            ),
            Self::Integrity {
                phase,
                name,
                detail,
            } => write!(
                f,
                "{phase} integrity failure for {:?}: {detail}",
                display_name(name)
            ),
            Self::VerificationMismatch { name, detail } => {
                write!(
                    f,
                    "rebuilt entry {:?} differs: {detail}",
                    display_name(name)
                )
            }
        }
    }
}

impl std::error::Error for ZipRebuildError {
    fn source(&self) -> Option<&(dyn std::error::Error + 'static)> {
        match self {
            Self::Zip { source, .. } => Some(source),
            Self::Io { source, .. } => Some(source),
            _ => None,
        }
    }
}

struct SourceEntry<'a> {
    name: Vec<u8>,
    method: CompressionMethod,
    crc32: u32,
    compressed_size: u64,
    uncompressed_size: u64,
    local_header_offset: u64,
    compressed_data_range: (u64, u64),
    raw_compressed: &'a [u8],
    local_record_range: (u64, u64),
    central_record_range: (u64, u64),
}

/// One source archive entry as it was declared, in central-directory order.
///
/// Offsets are relative to the start of the logical ZIP, matching the slice
/// handed to [`rebuild_zip`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ZipEntryManifest {
    pub name: Vec<u8>,
    pub compression_method: u16,
    pub crc32: u32,
    pub compressed_bytes: u64,
    pub uncompressed_bytes: u64,
    pub compressed_data_start: u64,
    pub compressed_data_end: u64,
}

/// The rebuilt archive plus the validated manifest of the source archive.
#[derive(Debug, Clone)]
pub struct ZipRebuildOutcome {
    pub bytes: Vec<u8>,
    pub source_entries: Vec<ZipEntryManifest>,
}

#[derive(Debug, Clone, Copy)]
struct CentralRecordManifest {
    range: (u64, u64),
    internal_attributes: u16,
    external_attributes: u32,
    uses_zip64_sizes: bool,
}

#[derive(Debug, Clone, Copy)]
struct VerificationBounds {
    phase: &'static str,
    total_resource: &'static str,
    declared_size: u64,
    verified_before: u64,
    total_budget: u64,
}

#[derive(Debug)]
struct WriteLimitExceeded {
    resource: &'static str,
    limit: u64,
    attempted: u64,
}

impl fmt::Display for WriteLimitExceeded {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(
            f,
            "{} write would exceed its limit: {} > {}",
            self.resource, self.attempted, self.limit
        )
    }
}

impl std::error::Error for WriteLimitExceeded {}

/// Refuses a write in full before delegating if it would cross the budget.
struct BudgetWriter<W> {
    inner: W,
    resource: &'static str,
    limit: u64,
    base: u64,
    written: u64,
}

impl<W> BudgetWriter<W> {
    fn new(inner: W, resource: &'static str, limit: u64, base: u64) -> Self {
        Self {
            inner,
            resource,
            limit,
            base,
            written: 0,
        }
    }
}

impl<W: Write> Write for BudgetWriter<W> {
    fn write(&mut self, buffer: &[u8]) -> io::Result<usize> {
        let requested = u64::try_from(buffer.len()).unwrap_or(u64::MAX);
        let attempted = self
            .base
            .checked_add(self.written)
            .and_then(|current| current.checked_add(requested))
            .unwrap_or(u64::MAX);
        if attempted > self.limit {
            return Err(io::Error::new(
                io::ErrorKind::FileTooLarge,
                WriteLimitExceeded {
                    resource: self.resource,
                    limit: self.limit,
                    attempted,
                },
            ));
        }

        let written = self.inner.write(buffer)?;
        self.written = self
            .written
            .checked_add(u64::try_from(written).unwrap_or(u64::MAX))
            .unwrap_or(u64::MAX);
        Ok(written)
    }

    fn flush(&mut self) -> io::Result<()> {
        self.inner.flush()
    }
}

/// Rebuilds a logical ZIP archive, replacing named entries with new plain data.
///
/// Unchanged entries retain their exact compressed payload.  Replacements are
/// written as raw DEFLATE.  Every source and output entry is decompressed
/// through rawzip's CRC/size verifier before this function succeeds.
pub fn rebuild_zip(
    source_zip: &[u8],
    replacements: &BTreeMap<Vec<u8>, Vec<u8>>,
    limits: ZipRebuildLimits,
) -> Result<Vec<u8>, ZipRebuildError> {
    rebuild_zip_detailed(source_zip, replacements, limits).map(|outcome| outcome.bytes)
}

/// Rebuilds a logical ZIP archive and also reports the validated source manifest.
///
/// Passing an empty replacement map turns this into a pure inspection pass that
/// still exercises every source-side structural and CRC check.
pub fn rebuild_zip_detailed(
    source_zip: &[u8],
    replacements: &BTreeMap<Vec<u8>, Vec<u8>>,
    limits: ZipRebuildLimits,
) -> Result<ZipRebuildOutcome, ZipRebuildError> {
    let archive = ZipArchive::from_slice(source_zip).map_err(|source| ZipRebuildError::Zip {
        context: "locate source ZIP",
        source,
    })?;

    validate_single_disk(source_zip, archive.eocd_offset())?;
    let source_len = u64::try_from(source_zip.len()).unwrap_or(u64::MAX);
    if archive.end_offset() != source_len {
        return Err(ZipRebuildError::TrailingData {
            archive_end: archive.end_offset(),
            source_len,
        });
    }

    let declared = archive.entries_hint();
    enforce_limit("entry count", limits.max_entries as u64, declared)?;

    let mut entries = Vec::new();
    let mut names = BTreeSet::new();
    let mut compressed_total = 0_u64;
    let mut uncompressed_total = 0_u64;
    let mut verified_uncompressed_total = 0_u64;
    let mut directory = archive.entries();
    while let Some(header) = directory
        .next_entry()
        .map_err(|source| ZipRebuildError::Zip {
            context: "read source central directory",
            source,
        })?
    {
        if entries.len() >= limits.max_entries {
            return Err(ZipRebuildError::LimitExceeded {
                resource: "entry count",
                limit: limits.max_entries as u64,
                actual: entries.len() as u64 + 1,
            });
        }

        let name = header.file_path().as_ref().to_vec();
        if !names.insert(name.clone()) {
            return Err(ZipRebuildError::DuplicateName(name));
        }
        if header.is_dir() {
            return Err(ZipRebuildError::DirectoryEntry(name));
        }
        let flags = header.flags();
        if flags.is_encrypted() || flags.has_strong_encryption() || flags.is_masked() {
            return Err(ZipRebuildError::EncryptedEntry(name));
        }
        if flags.bits() & !FLAG_DATA_DESCRIPTOR != 0 {
            return Err(ZipRebuildError::UnsupportedEntryFlags {
                name,
                flags: flags.bits(),
            });
        }
        let method = header.compression_method();
        if method != CompressionMethod::STORE && method != CompressionMethod::DEFLATE {
            return Err(ZipRebuildError::UnsupportedCompression {
                name,
                method: method.as_u16(),
            });
        }
        validate_central_disk_start(source_zip, header.central_directory_offset())?;
        let central_manifest =
            central_record_manifest(source_zip, header.central_directory_offset(), &name)?;
        if !header.extra_fields().remaining_bytes().is_empty() {
            return Err(ZipRebuildError::UnsupportedEntryMetadata {
                name,
                field: "central extra fields",
            });
        }
        if !header.comment().as_bytes().is_empty() {
            return Err(ZipRebuildError::UnsupportedEntryMetadata {
                name,
                field: "entry comment",
            });
        }
        let central_dos = header.last_modified_dos();
        if central_dos.packed_time() != 0 || central_dos.packed_date() != 0 {
            return Err(ZipRebuildError::UnsupportedEntryMetadata {
                name,
                field: "central DOS timestamp",
            });
        }
        if central_manifest.external_attributes != 0 {
            return Err(ZipRebuildError::UnsupportedEntryMetadata {
                name,
                field: "external attributes",
            });
        }
        if central_manifest.internal_attributes != 0 {
            return Err(ZipRebuildError::UnsupportedEntryMetadata {
                name,
                field: "internal attributes",
            });
        }

        let local =
            archive
                .get_entry(header.wayfinder())
                .map_err(|source| ZipRebuildError::Zip {
                    context: "read source local entry",
                    source,
                })?;
        let local_header = local.local_header();
        if local_header.file_path().as_ref() != name.as_slice() {
            return Err(ZipRebuildError::LocalCentralNameMismatch(name));
        }
        if local_header.compression_method() != method {
            return Err(ZipRebuildError::Integrity {
                phase: "source",
                name,
                detail: "local and central compression methods disagree".to_string(),
            });
        }
        let local_flags = local_header.flags();
        if local_flags.bits() != flags.bits() {
            return Err(ZipRebuildError::EntryFlagsMismatch {
                name,
                central: flags.bits(),
                local: local_flags.bits(),
            });
        }
        if local_flags.is_encrypted()
            || local_flags.has_strong_encryption()
            || local_flags.is_masked()
        {
            return Err(ZipRebuildError::EncryptedEntry(name));
        }
        if !local_header.extra_fields().remaining_bytes().is_empty() {
            return Err(ZipRebuildError::UnsupportedEntryMetadata {
                name,
                field: "local extra fields",
            });
        }
        let local_dos = local_header.last_modified_dos();
        if local_dos.packed_time() != 0 || local_dos.packed_date() != 0 {
            return Err(ZipRebuildError::UnsupportedEntryMetadata {
                name,
                field: "local DOS timestamp",
            });
        }
        if !flags.has_data_descriptor() {
            require_local_central_value(
                &name,
                "CRC32",
                u64::from(local_header.crc32()),
                u64::from(header.crc32()),
            )?;
            require_local_central_value(
                &name,
                "compressed size",
                local_header.compressed_size_hint(),
                header.compressed_size_hint(),
            )?;
            require_local_central_value(
                &name,
                "uncompressed size",
                local_header.uncompressed_size_hint(),
                header.uncompressed_size_hint(),
            )?;
        }
        if let Some(descriptor) =
            local
                .data_descriptor()
                .map_err(|source| ZipRebuildError::Zip {
                    context: "read source data descriptor",
                    source,
                })?
            && (descriptor.crc32() != header.crc32()
                || descriptor.compressed_size() != header.compressed_size_hint()
                || descriptor.uncompressed_size() != header.uncompressed_size_hint())
        {
            return Err(ZipRebuildError::Integrity {
                phase: "source",
                name,
                detail: "data descriptor and central directory disagree".to_string(),
            });
        }
        let compressed_range = local.compressed_data_range();
        let expected_data_start = header
            .local_header_offset()
            .checked_add(LOCAL_HEADER_BYTES)
            .and_then(|offset| offset.checked_add(name.len() as u64))
            .ok_or_else(|| {
                ZipRebuildError::ArchiveLayout(format!(
                    "local header offset overflows for {name:?}"
                ))
            })?;
        if compressed_range.0 != expected_data_start {
            return Err(ZipRebuildError::ArchiveLayout(format!(
                "local data for {name:?} starts at {}, expected {expected_data_start}",
                compressed_range.0
            )));
        }
        let expected_compressed_end = expected_data_start
            .checked_add(header.compressed_size_hint())
            .ok_or_else(|| {
                ZipRebuildError::ArchiveLayout(format!(
                    "compressed-data range overflows for {name:?}"
                ))
            })?;
        if compressed_range.1 != expected_compressed_end {
            return Err(ZipRebuildError::ArchiveLayout(format!(
                "compressed data for {name:?} ends at {}, expected {expected_compressed_end}",
                compressed_range.1
            )));
        }
        let local_record_end = data_descriptor_end(
            source_zip,
            compressed_range.1,
            flags.has_data_descriptor(),
            central_manifest.uses_zip64_sizes,
            &name,
        )?;

        compressed_total = checked_total(
            "total compressed bytes",
            compressed_total,
            header.compressed_size_hint(),
            limits.max_total_compressed_bytes,
        )?;
        uncompressed_total = checked_total(
            "total uncompressed bytes",
            uncompressed_total,
            header.uncompressed_size_hint(),
            limits.max_total_uncompressed_bytes,
        )?;
        let verified = verify_local(
            &local,
            method,
            None,
            &name,
            VerificationBounds {
                phase: "source",
                total_resource: "source actual total uncompressed bytes",
                declared_size: header.uncompressed_size_hint(),
                verified_before: verified_uncompressed_total,
                total_budget: limits.max_total_uncompressed_bytes,
            },
        )?;
        verified_uncompressed_total = checked_total(
            "source actual total uncompressed bytes",
            verified_uncompressed_total,
            verified,
            limits.max_total_uncompressed_bytes,
        )?;

        entries.push(SourceEntry {
            name,
            method,
            crc32: header.crc32(),
            compressed_size: header.compressed_size_hint(),
            uncompressed_size: header.uncompressed_size_hint(),
            local_header_offset: header.local_header_offset(),
            compressed_data_range: compressed_range,
            raw_compressed: local.data(),
            local_record_range: (header.local_header_offset(), local_record_end),
            central_record_range: central_manifest.range,
        });
    }

    if entries.len() as u64 != declared {
        return Err(ZipRebuildError::EntryCountMismatch {
            declared,
            actual: entries.len(),
        });
    }
    let first_offset = entries
        .iter()
        .map(|entry| entry.local_header_offset)
        .min()
        .unwrap_or_else(|| archive.directory_offset());
    if first_offset != 0 {
        return Err(ZipRebuildError::ArchivePrelude(first_offset));
    }
    validate_eocd_manifest(
        source_zip,
        archive.directory_offset(),
        archive.eocd_offset(),
    )?;
    validate_record_layout(&entries, archive.directory_offset(), archive.eocd_offset())?;
    for target in replacements.keys() {
        if !names.contains(target) {
            return Err(ZipRebuildError::MissingReplacement(target.clone()));
        }
    }

    let mut expected_uncompressed = 0_u64;
    for entry in &entries {
        let size = replacements
            .get(&entry.name)
            .map_or(entry.uncompressed_size, |data| data.len() as u64);
        expected_uncompressed = checked_total(
            "rebuilt total uncompressed bytes",
            expected_uncompressed,
            size,
            limits.max_total_uncompressed_bytes,
        )?;
    }

    let archive_output_limit = rebuilt_archive_budget(
        &entries,
        archive.comment().as_bytes().len(),
        limits.max_total_compressed_bytes,
    )?;
    let mut output = Vec::new();
    let mut output_compressed = 0_u64;
    {
        let bounded_output = BudgetWriter::new(
            &mut output,
            "rebuilt archive bytes",
            archive_output_limit,
            0,
        );
        let mut writer = ZipArchiveWriter::new(bounded_output);
        writer.set_comment(archive.comment().as_bytes());
        for entry in &entries {
            let (mut destination, config) = writer
                .new_file(EntryPath::verbatim(entry.name.as_slice()))
                .compression_method(if replacements.contains_key(&entry.name) {
                    CompressionMethod::DEFLATE
                } else {
                    entry.method
                })
                .start()
                .map_err(|source| map_output_zip("start rebuilt ZIP entry", source))?;

            let written = if let Some(replacement) = replacements.get(&entry.name) {
                let compressed = BudgetWriter::new(
                    &mut destination,
                    "rebuilt total compressed bytes",
                    limits.max_total_compressed_bytes,
                    output_compressed,
                );
                let encoder = DeflateEncoder::new(compressed, flate2::Compression::default());
                let mut plain = config.wrap(encoder);
                plain
                    .write_all(replacement)
                    .map_err(|source| map_output_io("compress replacement entry", source))?;
                let (encoder, descriptor) = plain
                    .finish()
                    .map_err(|source| map_output_zip("finalize replacement checksum", source))?;
                let _ = encoder
                    .finish()
                    .map_err(|source| map_output_io("finish replacement DEFLATE stream", source))?;
                destination
                    .finish(descriptor)
                    .map_err(|source| map_output_zip("finish replacement ZIP entry", source))?
            } else {
                let _ = config;
                {
                    let mut compressed = BudgetWriter::new(
                        &mut destination,
                        "rebuilt total compressed bytes",
                        limits.max_total_compressed_bytes,
                        output_compressed,
                    );
                    compressed
                        .write_all(entry.raw_compressed)
                        .map_err(|source| map_output_io("copy compressed ZIP entry", source))?;
                }
                destination
                    .finish(DataDescriptorOutput::new(
                        entry.crc32,
                        entry.uncompressed_size,
                    ))
                    .map_err(|source| map_output_zip("finish copied ZIP entry", source))?
            };
            output_compressed = checked_total(
                "rebuilt total compressed bytes",
                output_compressed,
                written.compressed_size(),
                limits.max_total_compressed_bytes,
            )?;
        }
        writer
            .finish()
            .map_err(|source| map_output_zip("finish rebuilt ZIP", source))?;
    }

    verify_rebuilt(&output, &entries, replacements, limits)?;
    let source_entries = entries
        .iter()
        .map(|entry| ZipEntryManifest {
            name: entry.name.clone(),
            compression_method: entry.method.as_u16(),
            crc32: entry.crc32,
            compressed_bytes: entry.compressed_size,
            uncompressed_bytes: entry.uncompressed_size,
            compressed_data_start: entry.compressed_data_range.0,
            compressed_data_end: entry.compressed_data_range.1,
        })
        .collect();
    Ok(ZipRebuildOutcome {
        bytes: output,
        source_entries,
    })
}

fn enforce_limit(resource: &'static str, limit: u64, actual: u64) -> Result<(), ZipRebuildError> {
    if actual > limit {
        Err(ZipRebuildError::LimitExceeded {
            resource,
            limit,
            actual,
        })
    } else {
        Ok(())
    }
}

fn checked_total(
    resource: &'static str,
    current: u64,
    increment: u64,
    limit: u64,
) -> Result<u64, ZipRebuildError> {
    let actual = current.checked_add(increment).unwrap_or(u64::MAX);
    enforce_limit(resource, limit, actual)?;
    Ok(actual)
}

fn rebuilt_archive_budget(
    entries: &[SourceEntry<'_>],
    comment_len: usize,
    compressed_payload_limit: u64,
) -> Result<u64, ZipRebuildError> {
    // Conservatively includes ZIP64 EOCD+locator, a ZIP64 data descriptor and
    // maximum writer-generated ZIP64 central extra data for every entry.
    let mut overhead = 98_u64
        .checked_add(u64::try_from(comment_len).unwrap_or(u64::MAX))
        .ok_or(ZipRebuildError::LimitExceeded {
            resource: "rebuilt ZIP structural overhead",
            limit: u64::MAX,
            actual: u64::MAX,
        })?;
    for entry in entries {
        let names = u64::try_from(entry.name.len())
            .unwrap_or(u64::MAX)
            .checked_mul(2)
            .ok_or(ZipRebuildError::LimitExceeded {
                resource: "rebuilt ZIP structural overhead",
                limit: u64::MAX,
                actual: u64::MAX,
            })?;
        overhead = overhead
            .checked_add(128)
            .and_then(|value| value.checked_add(names))
            .ok_or(ZipRebuildError::LimitExceeded {
                resource: "rebuilt ZIP structural overhead",
                limit: u64::MAX,
                actual: u64::MAX,
            })?;
    }
    compressed_payload_limit
        .checked_add(overhead)
        .ok_or(ZipRebuildError::LimitExceeded {
            resource: "rebuilt archive byte budget",
            limit: u64::MAX,
            actual: u64::MAX,
        })
}

fn find_write_limit<'a>(
    mut error: &'a (dyn std::error::Error + 'static),
) -> Option<&'a WriteLimitExceeded> {
    loop {
        if let Some(limit) = error.downcast_ref::<WriteLimitExceeded>() {
            return Some(limit);
        }
        error = error.source()?;
    }
}

fn map_output_io(context: &'static str, source: io::Error) -> ZipRebuildError {
    let direct_limit = source
        .get_ref()
        .and_then(|error| error.downcast_ref::<WriteLimitExceeded>());
    if let Some(limit) = direct_limit.or_else(|| find_write_limit(&source)) {
        return ZipRebuildError::OutputLimitExceeded {
            resource: limit.resource,
            limit: limit.limit,
            attempted: limit.attempted,
        };
    }
    ZipRebuildError::Io { context, source }
}

fn map_output_zip(context: &'static str, source: rawzip::Error) -> ZipRebuildError {
    let direct_limit = match source.kind() {
        rawzip::ErrorKind::IO(error) => error
            .get_ref()
            .and_then(|inner| inner.downcast_ref::<WriteLimitExceeded>()),
        _ => None,
    };
    if let Some(limit) = direct_limit.or_else(|| find_write_limit(&source)) {
        return ZipRebuildError::OutputLimitExceeded {
            resource: limit.resource,
            limit: limit.limit,
            attempted: limit.attempted,
        };
    }
    ZipRebuildError::Zip { context, source }
}

fn central_record_manifest(
    source: &[u8],
    central_offset: u64,
    name: &[u8],
) -> Result<CentralRecordManifest, ZipRebuildError> {
    let offset = usize::try_from(central_offset).map_err(|_| {
        ZipRebuildError::ArchiveLayout(format!(
            "central-directory offset does not fit memory for {name:?}"
        ))
    })?;
    let fixed_end = offset.checked_add(CENTRAL_HEADER_BYTES).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!(
            "central-directory header offset overflows for {name:?}"
        ))
    })?;
    let fixed = source.get(offset..fixed_end).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!("truncated central-directory header for {name:?}"))
    })?;
    if u32::from_le_bytes(fixed[0..4].try_into().expect("fixed slice")) != 0x0201_4b50 {
        return Err(ZipRebuildError::ArchiveLayout(format!(
            "invalid central-directory signature for {name:?}"
        )));
    }

    let compressed_size = u32::from_le_bytes(fixed[20..24].try_into().expect("fixed slice"));
    let uncompressed_size = u32::from_le_bytes(fixed[24..28].try_into().expect("fixed slice"));
    let name_len = usize::from(u16::from_le_bytes(
        fixed[28..30].try_into().expect("fixed slice"),
    ));
    let extra_len = usize::from(u16::from_le_bytes(
        fixed[30..32].try_into().expect("fixed slice"),
    ));
    let comment_len = usize::from(u16::from_le_bytes(
        fixed[32..34].try_into().expect("fixed slice"),
    ));
    let internal_attributes = u16::from_le_bytes(fixed[36..38].try_into().expect("fixed slice"));
    let external_attributes = u32::from_le_bytes(fixed[38..42].try_into().expect("fixed slice"));
    let name_end = fixed_end.checked_add(name_len).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!(
            "central-directory name range overflows for {name:?}"
        ))
    })?;
    let extra_end = name_end.checked_add(extra_len).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!(
            "central-directory extra-field range overflows for {name:?}"
        ))
    })?;
    let record_end = extra_end.checked_add(comment_len).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!(
            "central-directory comment range overflows for {name:?}"
        ))
    })?;
    let record = source.get(offset..record_end).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!("truncated central-directory record for {name:?}"))
    })?;
    if &record[CENTRAL_HEADER_BYTES..CENTRAL_HEADER_BYTES + name_len] != name {
        return Err(ZipRebuildError::Integrity {
            phase: "source",
            name: name.to_vec(),
            detail: "raw central-directory name disagrees with parsed name".to_string(),
        });
    }

    Ok(CentralRecordManifest {
        range: (central_offset, record_end as u64),
        internal_attributes,
        external_attributes,
        uses_zip64_sizes: compressed_size == u32::MAX || uncompressed_size == u32::MAX,
    })
}

fn validate_eocd_manifest(
    source: &[u8],
    directory_offset: u64,
    eocd_offset: u64,
) -> Result<(), ZipRebuildError> {
    let offset = usize::try_from(eocd_offset).map_err(|_| {
        ZipRebuildError::ArchiveLayout("EOCD offset does not fit memory".to_string())
    })?;
    let end = offset
        .checked_add(EOCD_HEADER_BYTES)
        .ok_or_else(|| ZipRebuildError::ArchiveLayout("EOCD header range overflows".to_string()))?;
    let record = source
        .get(offset..end)
        .ok_or_else(|| ZipRebuildError::ArchiveLayout("truncated EOCD header".to_string()))?;
    if u32::from_le_bytes(record[0..4].try_into().expect("fixed slice")) != 0x0605_4b50 {
        return Err(ZipRebuildError::ArchiveLayout(
            "invalid EOCD signature".to_string(),
        ));
    }
    let declared_size = u32::from_le_bytes(record[12..16].try_into().expect("fixed slice"));
    let declared_offset = u32::from_le_bytes(record[16..20].try_into().expect("fixed slice"));
    if declared_size == u32::MAX || declared_offset == u32::MAX {
        return Err(ZipRebuildError::ArchiveLayout(
            "ZIP64 EOCD metadata is not safely rewritable".to_string(),
        ));
    }
    require_eocd_value(
        "central-directory offset",
        u64::from(declared_offset),
        directory_offset,
    )?;
    let actual_size = eocd_offset.checked_sub(directory_offset).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout("central directory begins after the EOCD record".to_string())
    })?;
    require_eocd_value(
        "central-directory size",
        u64::from(declared_size),
        actual_size,
    )
}

fn require_eocd_value(
    field: &'static str,
    declared: u64,
    actual: u64,
) -> Result<(), ZipRebuildError> {
    if declared != actual {
        return Err(ZipRebuildError::ArchiveLayout(format!(
            "EOCD {field} declares {declared}, actual value is {actual}"
        )));
    }
    Ok(())
}

fn require_local_central_value(
    name: &[u8],
    field: &'static str,
    local: u64,
    central: u64,
) -> Result<(), ZipRebuildError> {
    if local != central {
        return Err(ZipRebuildError::LocalCentralHeaderMismatch {
            name: name.to_vec(),
            field,
            local,
            central,
        });
    }
    Ok(())
}

fn data_descriptor_end(
    source: &[u8],
    compressed_end: u64,
    has_descriptor: bool,
    uses_zip64_sizes: bool,
    name: &[u8],
) -> Result<u64, ZipRebuildError> {
    if !has_descriptor {
        return Ok(compressed_end);
    }

    let start = usize::try_from(compressed_end).map_err(|_| {
        ZipRebuildError::ArchiveLayout(format!(
            "data-descriptor offset does not fit memory for {name:?}"
        ))
    })?;
    let prefix = source.get(start..start.saturating_add(4)).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!("truncated data descriptor for {name:?}"))
    })?;
    let has_signature = u32::from_le_bytes(prefix.try_into().expect("four-byte slice"))
        == DATA_DESCRIPTOR_SIGNATURE;
    let body_len = if uses_zip64_sizes { 20 } else { 12 };
    let descriptor_len = body_len + usize::from(has_signature) * 4;
    let end = start.checked_add(descriptor_len).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!("data-descriptor range overflows for {name:?}"))
    })?;
    source.get(start..end).ok_or_else(|| {
        ZipRebuildError::ArchiveLayout(format!("truncated data descriptor for {name:?}"))
    })?;
    Ok(end as u64)
}

fn validate_record_layout(
    entries: &[SourceEntry<'_>],
    directory_offset: u64,
    eocd_offset: u64,
) -> Result<(), ZipRebuildError> {
    let local_ranges = entries
        .iter()
        .map(|entry| entry.local_record_range)
        .collect();
    validate_contiguous_ranges(local_ranges, 0, directory_offset, "local entry records")?;

    let central_ranges = entries
        .iter()
        .map(|entry| entry.central_record_range)
        .collect();
    validate_contiguous_ranges(
        central_ranges,
        directory_offset,
        eocd_offset,
        "central-directory records",
    )
}

fn validate_contiguous_ranges(
    mut ranges: Vec<(u64, u64)>,
    expected_start: u64,
    expected_end: u64,
    region: &'static str,
) -> Result<(), ZipRebuildError> {
    if expected_start > expected_end {
        return Err(ZipRebuildError::ArchiveLayout(format!(
            "{region} start at {expected_start} after end at {expected_end}"
        )));
    }
    ranges.sort_unstable_by_key(|range| range.0);
    let mut cursor = expected_start;
    for (start, end) in ranges {
        if start > end {
            return Err(ZipRebuildError::ArchiveLayout(format!(
                "{region} contain an inverted range {start}..{end}"
            )));
        }
        if start < cursor {
            return Err(ZipRebuildError::OverlappingEntries);
        }
        if start > cursor {
            return Err(ZipRebuildError::ArchiveLayout(format!(
                "{region} contain an unclaimed gap {cursor}..{start}"
            )));
        }
        if end > expected_end {
            return Err(ZipRebuildError::ArchiveLayout(format!(
                "{region} cross their boundary at {expected_end}"
            )));
        }
        cursor = end;
    }
    if cursor != expected_end {
        return Err(ZipRebuildError::ArchiveLayout(format!(
            "{region} end at {cursor}, expected {expected_end}"
        )));
    }
    Ok(())
}

fn verify_local(
    local: &rawzip::ZipSliceEntry<'_>,
    method: CompressionMethod,
    expected: Option<&[u8]>,
    name: &[u8],
    bounds: VerificationBounds,
) -> Result<u64, ZipRebuildError> {
    let remaining_budget = bounds
        .total_budget
        .checked_sub(bounds.verified_before)
        .ok_or(ZipRebuildError::LimitExceeded {
            resource: bounds.total_resource,
            limit: bounds.total_budget,
            actual: bounds.verified_before,
        })?;
    let claim = local.claim_verifier();
    if claim.uncompressed_size != bounds.declared_size {
        return Err(ZipRebuildError::Integrity {
            phase: bounds.phase,
            name: name.to_vec(),
            detail: format!(
                "central-directory size claim changed during verification: {} != {}",
                claim.uncompressed_size, bounds.declared_size
            ),
        });
    }

    match method {
        CompressionMethod::STORE => drain_and_compare(
            local.data(),
            expected,
            name,
            claim.crc,
            remaining_budget,
            bounds,
        ),
        CompressionMethod::DEFLATE => {
            let decoder = DeflateDecoder::new(local.data());
            drain_and_compare(decoder, expected, name, claim.crc, remaining_budget, bounds)
        }
        _ => unreachable!("compression method was checked before verification"),
    }
}

fn drain_and_compare<R: Read>(
    mut reader: R,
    expected: Option<&[u8]>,
    name: &[u8],
    expected_crc32: u32,
    remaining_budget: u64,
    bounds: VerificationBounds,
) -> Result<u64, ZipRebuildError> {
    let mut offset = 0_usize;
    let mut actual = 0_u64;
    let mut crc32 = Crc32::new();
    let mut buffer = [0_u8; 64 * 1024];

    // Probe at most one byte past the tighter of the per-entry declaration and
    // the archive-wide remaining budget.  Slicing every read to the remaining
    // probe allowance prevents a final 64 KiB drain from crossing either
    // boundary before an overrun can be rejected.
    let probe_limit = bounds
        .declared_size
        .min(remaining_budget)
        .checked_add(1)
        .unwrap_or(u64::MAX);
    loop {
        let probe_remaining = probe_limit.saturating_sub(actual);
        if probe_remaining == 0 {
            break;
        }
        let request = usize::try_from(probe_remaining.min(buffer.len() as u64))
            .expect("request is bounded by the in-memory buffer length");
        let read =
            reader
                .read(&mut buffer[..request])
                .map_err(|error| ZipRebuildError::Integrity {
                    phase: bounds.phase,
                    name: name.to_vec(),
                    detail: error.to_string(),
                })?;
        if read == 0 {
            break;
        }

        actual = actual
            .checked_add(u64::try_from(read).unwrap_or(u64::MAX))
            .unwrap_or(u64::MAX);
        if actual > remaining_budget {
            return Err(ZipRebuildError::LimitExceeded {
                resource: bounds.total_resource,
                limit: bounds.total_budget,
                actual: bounds
                    .verified_before
                    .checked_add(actual)
                    .unwrap_or(u64::MAX),
            });
        }
        if actual > bounds.declared_size {
            return Err(ZipRebuildError::Integrity {
                phase: bounds.phase,
                name: name.to_vec(),
                detail: format!(
                    "uncompressed size overrun: actual is at least {actual}, declared {}",
                    bounds.declared_size
                ),
            });
        }

        crc32.update(&buffer[..read]);
        if let Some(expected) = expected {
            let end = offset.saturating_add(read);
            if expected.get(offset..end) != Some(&buffer[..read]) {
                return Err(ZipRebuildError::VerificationMismatch {
                    name: name.to_vec(),
                    detail: "uncompressed content",
                });
            }
            offset = end;
        }
    }
    if actual < bounds.declared_size {
        return Err(ZipRebuildError::Integrity {
            phase: bounds.phase,
            name: name.to_vec(),
            detail: format!(
                "uncompressed size underrun: actual {actual}, declared {}",
                bounds.declared_size
            ),
        });
    }
    if expected.is_some_and(|expected| offset != expected.len()) {
        return Err(ZipRebuildError::VerificationMismatch {
            name: name.to_vec(),
            detail: "uncompressed length",
        });
    }
    let actual_crc32 = crc32.checksum();
    if actual_crc32 != expected_crc32 {
        return Err(ZipRebuildError::Integrity {
            phase: bounds.phase,
            name: name.to_vec(),
            detail: format!(
                "CRC32 mismatch: actual {actual_crc32:#010x}, expected {expected_crc32:#010x}"
            ),
        });
    }
    Ok(actual)
}

fn verify_rebuilt(
    output: &[u8],
    source_entries: &[SourceEntry<'_>],
    replacements: &BTreeMap<Vec<u8>, Vec<u8>>,
    limits: ZipRebuildLimits,
) -> Result<(), ZipRebuildError> {
    let archive = ZipArchive::from_slice(output).map_err(|source| ZipRebuildError::Zip {
        context: "reopen rebuilt ZIP",
        source,
    })?;
    enforce_limit(
        "rebuilt entry count",
        limits.max_entries as u64,
        archive.entries_hint(),
    )?;
    let mut declared_uncompressed_total = 0_u64;
    let mut verified_uncompressed_total = 0_u64;
    let mut directory = archive.entries();
    for expected_entry in source_entries {
        let header = directory
            .next_entry()
            .map_err(|source| ZipRebuildError::Zip {
                context: "read rebuilt central directory",
                source,
            })?
            .ok_or(ZipRebuildError::EntryCountMismatch {
                declared: source_entries.len() as u64,
                actual: 0,
            })?;
        if header.file_path().as_ref() != expected_entry.name.as_slice() {
            return Err(ZipRebuildError::VerificationMismatch {
                name: expected_entry.name.clone(),
                detail: "name or order",
            });
        }
        let replacement = replacements.get(&expected_entry.name);
        let expected_method = if replacement.is_some() {
            CompressionMethod::DEFLATE
        } else {
            expected_entry.method
        };
        if header.compression_method() != expected_method {
            return Err(ZipRebuildError::VerificationMismatch {
                name: expected_entry.name.clone(),
                detail: "compression method",
            });
        }
        let local =
            archive
                .get_entry(header.wayfinder())
                .map_err(|source| ZipRebuildError::Zip {
                    context: "read rebuilt local entry",
                    source,
                })?;
        declared_uncompressed_total = checked_total(
            "rebuilt total uncompressed bytes",
            declared_uncompressed_total,
            header.uncompressed_size_hint(),
            limits.max_total_uncompressed_bytes,
        )?;
        if replacement.is_none()
            && (header.crc32() != expected_entry.crc32
                || header.uncompressed_size_hint() != expected_entry.uncompressed_size
                || local.data() != expected_entry.raw_compressed)
        {
            return Err(ZipRebuildError::VerificationMismatch {
                name: expected_entry.name.clone(),
                detail: "unchanged entry metadata or compressed payload",
            });
        }
        let verified = verify_local(
            &local,
            expected_method,
            replacement.map(Vec::as_slice),
            &expected_entry.name,
            VerificationBounds {
                phase: "rebuilt",
                total_resource: "rebuilt actual total uncompressed bytes",
                declared_size: header.uncompressed_size_hint(),
                verified_before: verified_uncompressed_total,
                total_budget: limits.max_total_uncompressed_bytes,
            },
        )?;
        verified_uncompressed_total = checked_total(
            "rebuilt actual total uncompressed bytes",
            verified_uncompressed_total,
            verified,
            limits.max_total_uncompressed_bytes,
        )?;
    }
    if directory
        .next_entry()
        .map_err(|source| ZipRebuildError::Zip {
            context: "finish rebuilt central directory",
            source,
        })?
        .is_some()
    {
        return Err(ZipRebuildError::EntryCountMismatch {
            declared: source_entries.len() as u64,
            actual: source_entries.len() + 1,
        });
    }
    Ok(())
}

fn validate_single_disk(source: &[u8], eocd_offset: u64) -> Result<(), ZipRebuildError> {
    let offset = usize::try_from(eocd_offset).map_err(|_| ZipRebuildError::MultiDiskArchive)?;
    let fixed = source
        .get(offset..offset.saturating_add(22))
        .ok_or_else(|| ZipRebuildError::Integrity {
            phase: "source",
            name: Vec::new(),
            detail: "truncated EOCD".to_string(),
        })?;
    let disk = u16::from_le_bytes([fixed[4], fixed[5]]);
    let directory_disk = u16::from_le_bytes([fixed[6], fixed[7]]);
    let entries_on_disk = u16::from_le_bytes([fixed[8], fixed[9]]);
    let total_entries = u16::from_le_bytes([fixed[10], fixed[11]]);
    if disk != 0 || directory_disk != 0 || entries_on_disk != total_entries {
        return Err(ZipRebuildError::MultiDiskArchive);
    }

    if let Some(locator) = offset
        .checked_sub(20)
        .and_then(|start| source.get(start..offset))
        && locator.get(..4) == Some(&0x0706_4b50_u32.to_le_bytes())
    {
        let directory_disk = u32::from_le_bytes(locator[4..8].try_into().unwrap());
        let total_disks = u32::from_le_bytes(locator[16..20].try_into().unwrap());
        if directory_disk != 0 || total_disks != 1 {
            return Err(ZipRebuildError::MultiDiskArchive);
        }
        let zip64_offset = u64::from_le_bytes(locator[8..16].try_into().unwrap());
        let zip64_offset =
            usize::try_from(zip64_offset).map_err(|_| ZipRebuildError::MultiDiskArchive)?;
        let record = source
            .get(zip64_offset..zip64_offset.saturating_add(56))
            .ok_or(ZipRebuildError::MultiDiskArchive)?;
        let disk = u32::from_le_bytes(record[16..20].try_into().unwrap());
        let directory_disk = u32::from_le_bytes(record[20..24].try_into().unwrap());
        let entries_on_disk = u64::from_le_bytes(record[24..32].try_into().unwrap());
        let total_entries = u64::from_le_bytes(record[32..40].try_into().unwrap());
        if disk != 0 || directory_disk != 0 || entries_on_disk != total_entries {
            return Err(ZipRebuildError::MultiDiskArchive);
        }
    }
    Ok(())
}

fn validate_central_disk_start(source: &[u8], central_offset: u64) -> Result<(), ZipRebuildError> {
    let offset = usize::try_from(central_offset).map_err(|_| ZipRebuildError::MultiDiskArchive)?;
    let fixed = source
        .get(offset..offset.saturating_add(46))
        .ok_or(ZipRebuildError::MultiDiskArchive)?;
    let disk = u16::from_le_bytes([fixed[34], fixed[35]]);
    if disk != 0 {
        return Err(ZipRebuildError::MultiDiskArchive);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use rawzip::Header;
    use rawzip::extra_fields::ExtraFieldId;
    use std::cell::Cell;
    use std::rc::Rc;

    struct ObservedReader<'a> {
        data: &'a [u8],
        offset: usize,
        max_request: Rc<Cell<usize>>,
        returned: Rc<Cell<usize>>,
    }

    impl Read for ObservedReader<'_> {
        fn read(&mut self, buffer: &mut [u8]) -> io::Result<usize> {
            self.max_request
                .set(self.max_request.get().max(buffer.len()));
            let available = self.data.len().saturating_sub(self.offset);
            let read = available.min(buffer.len());
            buffer[..read].copy_from_slice(&self.data[self.offset..self.offset + read]);
            self.offset += read;
            self.returned.set(self.returned.get() + read);
            Ok(read)
        }
    }

    fn observed_reader(data: &[u8]) -> (ObservedReader<'_>, Rc<Cell<usize>>, Rc<Cell<usize>>) {
        let max_request = Rc::new(Cell::new(0));
        let returned = Rc::new(Cell::new(0));
        (
            ObservedReader {
                data,
                offset: 0,
                max_request: Rc::clone(&max_request),
                returned: Rc::clone(&returned),
            },
            max_request,
            returned,
        )
    }

    fn make_zip(files: &[(&[u8], &[u8], CompressionMethod)]) -> Vec<u8> {
        let mut output = Vec::new();
        {
            let mut archive = ZipArchiveWriter::new(&mut output);
            for (name, data, method) in files {
                let (mut entry, config) = archive
                    .new_file(EntryPath::verbatim(*name))
                    .compression_method(*method)
                    .start()
                    .unwrap();
                if *method == CompressionMethod::DEFLATE {
                    let encoder = DeflateEncoder::new(&mut entry, flate2::Compression::fast());
                    let mut plain = config.wrap(encoder);
                    plain.write_all(data).unwrap();
                    let (encoder, descriptor) = plain.finish().unwrap();
                    encoder.finish().unwrap();
                    entry.finish(descriptor).unwrap();
                } else {
                    let mut plain = config.wrap(&mut entry);
                    plain.write_all(data).unwrap();
                    let (_, descriptor) = plain.finish().unwrap();
                    entry.finish(descriptor).unwrap();
                }
            }
            archive.finish().unwrap();
        }
        output
    }

    fn make_zip_with_metadata(extra_header: Option<Header>, comment: Option<&[u8]>) -> Vec<u8> {
        let mut output = Vec::new();
        {
            let mut archive = ZipArchiveWriter::new(&mut output);
            let mut builder = archive
                .new_file(EntryPath::verbatim(b"gamestate"))
                .compression_method(CompressionMethod::STORE);
            if let Some(header) = extra_header {
                builder = builder
                    .extra_field(ExtraFieldId::JAVA_JAR, b"x", header)
                    .unwrap();
            }
            if let Some(comment) = comment {
                builder = builder.comment(comment.to_vec());
            }
            let (mut entry, config) = builder.start().unwrap();
            let mut plain = config.wrap(&mut entry);
            plain.write_all(b"abc").unwrap();
            let (_, descriptor) = plain.finish().unwrap();
            entry.finish(descriptor).unwrap();
            archive.finish().unwrap();
        }
        output
    }

    fn single_entry_offsets(zip: &[u8]) -> (usize, usize, usize) {
        let archive = ZipArchive::from_slice(zip).unwrap();
        let header = archive.entries().next_entry().unwrap().unwrap();
        (
            header.local_header_offset() as usize,
            header.central_directory_offset() as usize,
            archive.eocd_offset() as usize,
        )
    }

    fn without_data_descriptor(zip: &[u8]) -> Vec<u8> {
        let (
            local_offset,
            data_end,
            central_offset,
            eocd_offset,
            crc32,
            compressed_size,
            uncompressed_size,
        ) = {
            let archive = ZipArchive::from_slice(zip).unwrap();
            let header = archive.entries().next_entry().unwrap().unwrap();
            assert_eq!(archive.entries_hint(), 1);
            let local = archive.get_entry(header.wayfinder()).unwrap();
            let local_offset = header.local_header_offset() as usize;
            let data_end = local.compressed_data_range().1 as usize;
            let central_offset = header.central_directory_offset() as usize;
            let eocd_offset = archive.eocd_offset() as usize;
            assert_eq!(central_offset - data_end, 16);
            (
                local_offset,
                data_end,
                central_offset,
                eocd_offset,
                header.crc32(),
                u32::try_from(header.compressed_size_hint()).unwrap(),
                u32::try_from(header.uncompressed_size_hint()).unwrap(),
            )
        };

        let descriptor_len = central_offset - data_end;
        let mut output = zip.to_vec();
        output.drain(data_end..central_offset);
        let shifted_central = central_offset - descriptor_len;
        let shifted_eocd = eocd_offset - descriptor_len;
        output[local_offset + 6..local_offset + 8].copy_from_slice(&0_u16.to_le_bytes());
        output[local_offset + 14..local_offset + 18].copy_from_slice(&crc32.to_le_bytes());
        output[local_offset + 18..local_offset + 22]
            .copy_from_slice(&compressed_size.to_le_bytes());
        output[local_offset + 22..local_offset + 26]
            .copy_from_slice(&uncompressed_size.to_le_bytes());
        output[shifted_central + 8..shifted_central + 10].copy_from_slice(&0_u16.to_le_bytes());
        output[shifted_eocd + 16..shifted_eocd + 20]
            .copy_from_slice(&(shifted_central as u32).to_le_bytes());
        output
    }

    fn raw_payload(zip: &[u8], wanted: &[u8]) -> Vec<u8> {
        let archive = ZipArchive::from_slice(zip).unwrap();
        for item in archive.entries() {
            let item = item.unwrap();
            if item.file_path().as_ref() == wanted {
                return archive.get_entry(item.wayfinder()).unwrap().data().to_vec();
            }
        }
        panic!("missing test entry")
    }

    fn names(zip: &[u8]) -> Vec<Vec<u8>> {
        ZipArchive::from_slice(zip)
            .unwrap()
            .entries()
            .map(|entry| entry.unwrap().file_path().as_ref().to_vec())
            .collect()
    }

    #[test]
    fn rebuild_preserves_order_and_unchanged_compressed_payload() {
        let source = make_zip(&[
            (b"meta", b"untouched metadata", CompressionMethod::DEFLATE),
            (b"gamestate", b"old", CompressionMethod::DEFLATE),
            (b"extra", b"also untouched", CompressionMethod::STORE),
        ]);
        let untouched = raw_payload(&source, b"meta");
        let mut replacements = BTreeMap::new();
        replacements.insert(b"gamestate".to_vec(), vec![b'x'; 16_384]);

        let rebuilt = rebuild_zip(&source, &replacements, ZipRebuildLimits::default()).unwrap();
        assert_eq!(names(&rebuilt), names(&source));
        assert_eq!(raw_payload(&rebuilt, b"meta"), untouched);

        let archive = ZipArchive::from_slice(&rebuilt).unwrap();
        let first = archive.entries().next_entry().unwrap().unwrap();
        assert_eq!(first.local_header_offset(), 0);
        let eocd = archive.eocd_offset() as usize;
        let stored_cd = u32::from_le_bytes(rebuilt[eocd + 16..eocd + 20].try_into().unwrap());
        assert_eq!(u64::from(stored_cd), archive.directory_offset());
    }

    #[test]
    fn accepts_only_safe_flag_forms_and_requires_matching_headers() {
        let descriptor_source = make_zip(&[(b"gamestate", b"abc", CompressionMethod::STORE)]);
        let rebuilt = rebuild_zip(
            &descriptor_source,
            &BTreeMap::new(),
            ZipRebuildLimits::default(),
        )
        .unwrap();
        let archive = ZipArchive::from_slice(&rebuilt).unwrap();
        let header = archive.entries().next_entry().unwrap().unwrap();
        assert_eq!(header.flags().bits(), FLAG_DATA_DESCRIPTOR);

        let plain_source = without_data_descriptor(&descriptor_source);
        let rebuilt =
            rebuild_zip(&plain_source, &BTreeMap::new(), ZipRebuildLimits::default()).unwrap();
        assert_eq!(raw_payload(&rebuilt, b"gamestate"), b"abc");

        let (local, central, _) = single_entry_offsets(&descriptor_source);
        let mut mismatched = descriptor_source.clone();
        mismatched[local + 6..local + 8].copy_from_slice(&0_u16.to_le_bytes());
        assert!(matches!(
            rebuild_zip(&mismatched, &BTreeMap::new(), ZipRebuildLimits::default()),
            Err(ZipRebuildError::EntryFlagsMismatch {
                central: FLAG_DATA_DESCRIPTOR,
                local: 0,
                ..
            })
        ));

        let mut unsupported = descriptor_source.clone();
        let unsupported_flags = FLAG_DATA_DESCRIPTOR | (1 << 11);
        unsupported[local + 6..local + 8].copy_from_slice(&unsupported_flags.to_le_bytes());
        unsupported[central + 8..central + 10].copy_from_slice(&unsupported_flags.to_le_bytes());
        assert!(matches!(
            rebuild_zip(
                &unsupported,
                &BTreeMap::new(),
                ZipRebuildLimits::default()
            ),
            Err(ZipRebuildError::UnsupportedEntryFlags { flags, .. })
                if flags == unsupported_flags
        ));
    }

    #[test]
    fn rejects_local_claim_mismatches_without_data_descriptor() {
        let descriptor_source = make_zip(&[(b"gamestate", b"abc", CompressionMethod::STORE)]);
        let source = without_data_descriptor(&descriptor_source);
        let (local, _, _) = single_entry_offsets(&source);
        for (field, offset) in [
            ("CRC32", 14),
            ("compressed size", 18),
            ("uncompressed size", 22),
        ] {
            let mut attacked = source.clone();
            attacked[local + offset] ^= 1;
            let result = rebuild_zip(&attacked, &BTreeMap::new(), ZipRebuildLimits::default());
            assert!(
                matches!(
                    result,
                    Err(ZipRebuildError::LocalCentralHeaderMismatch {
                        field: actual,
                        ..
                    }) if actual == field
                ),
                "unexpected result for {field}: {result:?}"
            );
        }
    }

    #[test]
    fn rejects_entry_metadata_the_writer_would_drop() {
        for (source, field) in [
            (
                make_zip_with_metadata(Some(Header::LOCAL), None),
                "local extra fields",
            ),
            (
                make_zip_with_metadata(Some(Header::CENTRAL), None),
                "central extra fields",
            ),
            (
                make_zip_with_metadata(None, Some(b"hidden comment")),
                "entry comment",
            ),
        ] {
            let result = rebuild_zip(&source, &BTreeMap::new(), ZipRebuildLimits::default());
            assert!(
                matches!(
                    result,
                    Err(ZipRebuildError::UnsupportedEntryMetadata {
                        field: actual,
                        ..
                    }) if actual == field
                ),
                "unexpected result for {field}: {result:?}"
            );
        }

        let source = make_zip(&[(b"gamestate", b"abc", CompressionMethod::STORE)]);
        let (local, central, _) = single_entry_offsets(&source);
        for (field, offset, width) in [
            ("central DOS timestamp", central + 12, 4),
            ("local DOS timestamp", local + 10, 4),
            ("internal attributes", central + 36, 2),
            ("external attributes", central + 38, 4),
        ] {
            let mut attacked = source.clone();
            attacked[offset..offset + width].fill(1);
            let result = rebuild_zip(&attacked, &BTreeMap::new(), ZipRebuildLimits::default());
            assert!(
                matches!(
                    result,
                    Err(ZipRebuildError::UnsupportedEntryMetadata {
                        field: actual,
                        ..
                    }) if actual == field
                ),
                "unexpected result for {field}: {result:?}"
            );
        }
    }

    #[test]
    fn rejects_unclaimed_record_gaps_and_overlaps() {
        let source = make_zip(&[(b"gamestate", b"abc", CompressionMethod::STORE)]);
        let (_, central, eocd) = single_entry_offsets(&source);

        let mut wrong_directory_size = source.clone();
        wrong_directory_size[eocd + 12] ^= 1;
        let result = rebuild_zip(
            &wrong_directory_size,
            &BTreeMap::new(),
            ZipRebuildLimits::default(),
        );
        assert!(
            matches!(
                result,
                Err(ZipRebuildError::ArchiveLayout(ref detail))
                    if detail.contains("central-directory size")
            ),
            "unexpected EOCD-size result: {result:?}"
        );

        let mut attacked = source.clone();
        attacked.insert(central, 0);
        let shifted_central = central + 1;
        let shifted_eocd = eocd + 1;
        attacked[shifted_eocd + 16..shifted_eocd + 20]
            .copy_from_slice(&(shifted_central as u32).to_le_bytes());
        let result = rebuild_zip(&attacked, &BTreeMap::new(), ZipRebuildLimits::default());
        assert!(
            matches!(result, Err(ZipRebuildError::ArchiveLayout(_))),
            "unexpected gap result: {result:?}"
        );

        assert!(matches!(
            validate_contiguous_ranges(vec![(0, 10), (9, 20)], 0, 20, "test records"),
            Err(ZipRebuildError::OverlappingEntries)
        ));
    }

    #[test]
    fn rejects_bad_crc_duplicate_missing_unknown_method_and_limits() {
        let source = make_zip(&[(b"gamestate", b"abc", CompressionMethod::STORE)]);
        let archive = ZipArchive::from_slice(&source).unwrap();
        let central = archive
            .entries()
            .next_entry()
            .unwrap()
            .unwrap()
            .central_directory_offset() as usize;
        let mut bad_crc = source.clone();
        bad_crc[central + 16] ^= 0x80;
        assert!(matches!(
            rebuild_zip(&bad_crc, &BTreeMap::new(), ZipRebuildLimits::default()),
            Err(ZipRebuildError::Integrity { .. })
        ));

        let duplicate = make_zip(&[
            (b"same", b"one", CompressionMethod::STORE),
            (b"same", b"two", CompressionMethod::STORE),
        ]);
        assert!(matches!(
            rebuild_zip(&duplicate, &BTreeMap::new(), ZipRebuildLimits::default()),
            Err(ZipRebuildError::DuplicateName(_))
        ));

        let mut missing = BTreeMap::new();
        missing.insert(b"missing".to_vec(), b"x".to_vec());
        assert!(matches!(
            rebuild_zip(&source, &missing, ZipRebuildLimits::default()),
            Err(ZipRebuildError::MissingReplacement(_))
        ));

        let mut unknown = source.clone();
        unknown[8..10].copy_from_slice(&99_u16.to_le_bytes());
        unknown[central + 10..central + 12].copy_from_slice(&99_u16.to_le_bytes());
        assert!(matches!(
            rebuild_zip(&unknown, &BTreeMap::new(), ZipRebuildLimits::default()),
            Err(ZipRebuildError::UnsupportedCompression { method: 99, .. })
        ));

        let tiny = ZipRebuildLimits {
            max_entries: 0,
            ..ZipRebuildLimits::default()
        };
        assert!(matches!(
            rebuild_zip(&source, &BTreeMap::new(), tiny),
            Err(ZipRebuildError::LimitExceeded { .. })
        ));

        let compressed_tiny = ZipRebuildLimits {
            max_total_compressed_bytes: 2,
            ..ZipRebuildLimits::default()
        };
        assert!(matches!(
            rebuild_zip(&source, &BTreeMap::new(), compressed_tiny),
            Err(ZipRebuildError::LimitExceeded {
                resource: "total compressed bytes",
                ..
            })
        ));

        let uncompressed_tiny = ZipRebuildLimits {
            max_total_uncompressed_bytes: 2,
            ..ZipRebuildLimits::default()
        };
        assert!(matches!(
            rebuild_zip(&source, &BTreeMap::new(), uncompressed_tiny),
            Err(ZipRebuildError::LimitExceeded {
                resource: "total uncompressed bytes",
                ..
            })
        ));
    }

    #[test]
    fn output_budget_refuses_before_vec_growth() {
        assert_eq!(
            ZipRebuildLimits::default().max_total_compressed_bytes,
            256 * 1024 * 1024
        );
        assert_eq!(
            ZipRebuildLimits::default().max_total_uncompressed_bytes,
            512 * 1024 * 1024
        );

        let mut sink = Vec::new();
        {
            let mut bounded = BudgetWriter::new(&mut sink, "test output", 4, 0);
            assert!(bounded.write_all(b"12345").is_err());
        }
        assert!(sink.is_empty(), "rejected writes must not grow the Vec");

        let source = make_zip(&[(b"gamestate", b"x", CompressionMethod::STORE)]);
        let mut state = 0x1234_5678_u32;
        let replacement: Vec<u8> = (0..64 * 1024)
            .map(|_| {
                state ^= state << 13;
                state ^= state >> 17;
                state ^= state << 5;
                state as u8
            })
            .collect();
        let mut replacements = BTreeMap::new();
        replacements.insert(b"gamestate".to_vec(), replacement);
        let limits = ZipRebuildLimits {
            max_total_compressed_bytes: 1,
            max_total_uncompressed_bytes: 128 * 1024,
            ..ZipRebuildLimits::default()
        };
        let result = rebuild_zip(&source, &replacements, limits);
        assert!(
            matches!(
                &result,
                Err(ZipRebuildError::OutputLimitExceeded {
                    resource: "rebuilt total compressed bytes",
                    limit: 1,
                    ..
                })
            ),
            "unexpected rebuild result: {result:?}"
        );
    }

    #[test]
    fn decompression_verification_probes_only_one_byte_past_each_budget() {
        let oversized = vec![b'x'; 64 * 1024];
        let (reader, max_request, returned) = observed_reader(&oversized);
        let result = drain_and_compare(
            reader,
            None,
            b"gamestate",
            0,
            100,
            VerificationBounds {
                phase: "source",
                total_resource: "source actual total uncompressed bytes",
                declared_size: 3,
                verified_before: 0,
                total_budget: 100,
            },
        );
        assert!(matches!(
            result,
            Err(ZipRebuildError::Integrity { detail, .. })
                if detail.contains("size overrun")
        ));
        assert_eq!(max_request.get(), 4);
        assert_eq!(returned.get(), 4);

        let (reader, max_request, returned) = observed_reader(&oversized);
        let result = drain_and_compare(
            reader,
            None,
            b"gamestate",
            0,
            3,
            VerificationBounds {
                phase: "rebuilt",
                total_resource: "rebuilt actual total uncompressed bytes",
                declared_size: 100,
                verified_before: 7,
                total_budget: 10,
            },
        );
        assert!(matches!(
            result,
            Err(ZipRebuildError::LimitExceeded {
                resource: "rebuilt actual total uncompressed bytes",
                limit: 10,
                actual: 11,
            })
        ));
        assert_eq!(max_request.get(), 4);
        assert_eq!(returned.get(), 4);

        let short = b"xy";
        let (reader, max_request, returned) = observed_reader(short);
        let result = drain_and_compare(
            reader,
            None,
            b"gamestate",
            rawzip::crc32(short),
            100,
            VerificationBounds {
                phase: "source",
                total_resource: "source actual total uncompressed bytes",
                declared_size: 3,
                verified_before: 0,
                total_budget: 100,
            },
        );
        assert!(matches!(
            result,
            Err(ZipRebuildError::Integrity { detail, .. })
                if detail.contains("size underrun")
        ));
        assert_eq!(max_request.get(), 4);
        assert_eq!(returned.get(), 2);

        let exact = b"xyz";
        let (reader, max_request, returned) = observed_reader(exact);
        assert_eq!(
            drain_and_compare(
                reader,
                Some(exact),
                b"gamestate",
                rawzip::crc32(exact),
                3,
                VerificationBounds {
                    phase: "rebuilt",
                    total_resource: "rebuilt actual total uncompressed bytes",
                    declared_size: 3,
                    verified_before: 0,
                    total_budget: 3,
                },
            )
            .unwrap(),
            3
        );
        assert_eq!(max_request.get(), 4);
        assert_eq!(returned.get(), 3);
    }

    #[test]
    #[ignore = "requires a private real CK3 save selected by CK3_REAL_SAVE"]
    fn real_ck3_baseline_manifest_smoke() {
        use jomini::envelope::JominiFile;

        let source = std::fs::read(std::env::var_os("CK3_REAL_SAVE").unwrap()).unwrap();
        let save = JominiFile::from_slice(&source).unwrap();
        let zip_start = save
            .header()
            .header_len()
            .checked_add(usize::try_from(save.header().metadata_len()).unwrap())
            .unwrap();
        assert!(source[zip_start..].starts_with(b"PK\x03\x04"));
        let rebuilt = rebuild_zip(
            &source[zip_start..],
            &BTreeMap::new(),
            ZipRebuildLimits::default(),
        )
        .unwrap();
        assert_eq!(names(&rebuilt), names(&source[zip_start..]));
    }
}
