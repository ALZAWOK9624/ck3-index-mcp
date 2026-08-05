#![forbid(unsafe_code)]

//! A small, dependency-free SHA-256 implementation.
//!
//! The implementation follows FIPS 180-4 and rejects messages whose encoded
//! bit length would not fit in the standard 64-bit SHA-256 length field.

use std::convert::TryFrom;
use std::error::Error;
use std::fmt;
use std::io::{self, Read};

/// The largest whole-byte message representable by SHA-256's 64-bit bit count.
pub const MAX_MESSAGE_BYTES: u64 = u64::MAX / 8;

const BLOCK_BYTES: usize = 64;
const LENGTH_FIELD_OFFSET: usize = 56;

const INITIAL_STATE: [u32; 8] = [
    0x6a09_e667,
    0xbb67_ae85,
    0x3c6e_f372,
    0xa54f_f53a,
    0x510e_527f,
    0x9b05_688c,
    0x1f83_d9ab,
    0x5be0_cd19,
];

const ROUND_CONSTANTS: [u32; 64] = [
    0x428a_2f98,
    0x7137_4491,
    0xb5c0_fbcf,
    0xe9b5_dba5,
    0x3956_c25b,
    0x59f1_11f1,
    0x923f_82a4,
    0xab1c_5ed5,
    0xd807_aa98,
    0x1283_5b01,
    0x2431_85be,
    0x550c_7dc3,
    0x72be_5d74,
    0x80de_b1fe,
    0x9bdc_06a7,
    0xc19b_f174,
    0xe49b_69c1,
    0xefbe_4786,
    0x0fc1_9dc6,
    0x240c_a1cc,
    0x2de9_2c6f,
    0x4a74_84aa,
    0x5cb0_a9dc,
    0x76f9_88da,
    0x983e_5152,
    0xa831_c66d,
    0xb003_27c8,
    0xbf59_7fc7,
    0xc6e0_0bf3,
    0xd5a7_9147,
    0x06ca_6351,
    0x1429_2967,
    0x27b7_0a85,
    0x2e1b_2138,
    0x4d2c_6dfc,
    0x5338_0d13,
    0x650a_7354,
    0x766a_0abb,
    0x81c2_c92e,
    0x9272_2c85,
    0xa2bf_e8a1,
    0xa81a_664b,
    0xc24b_8b70,
    0xc76c_51a3,
    0xd192_e819,
    0xd699_0624,
    0xf40e_3585,
    0x106a_a070,
    0x19a4_c116,
    0x1e37_6c08,
    0x2748_774c,
    0x34b0_bcb5,
    0x391c_0cb3,
    0x4ed8_aa4a,
    0x5b9c_ca4f,
    0x682e_6ff3,
    0x748f_82ee,
    0x78a5_636f,
    0x84c8_7814,
    0x8cc7_0208,
    0x90be_fffa,
    0xa450_6ceb,
    0xbef9_a3f7,
    0xc671_78f2,
];

/// Returned when a message would exceed SHA-256's 64-bit bit-length field.
#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub struct MessageTooLong;

impl fmt::Display for MessageTooLong {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str("message is too long for SHA-256")
    }
}

impl Error for MessageTooLong {}

/// Incremental SHA-256 state.
#[derive(Clone)]
pub struct Sha256 {
    state: [u32; 8],
    buffer: [u8; BLOCK_BYTES],
    buffered: usize,
    message_bytes: u64,
}

impl Sha256 {
    /// Creates a fresh SHA-256 state.
    #[must_use]
    pub fn new() -> Self {
        Self {
            state: INITIAL_STATE,
            buffer: [0; BLOCK_BYTES],
            buffered: 0,
            message_bytes: 0,
        }
    }

    /// Adds bytes to the message.
    ///
    /// The length is validated before any state is changed, so an error leaves
    /// the hasher untouched.
    pub fn update(&mut self, mut input: &[u8]) -> Result<(), MessageTooLong> {
        let input_bytes = u64::try_from(input.len()).map_err(|_| MessageTooLong)?;
        let new_message_bytes = self
            .message_bytes
            .checked_add(input_bytes)
            .filter(|length| *length <= MAX_MESSAGE_BYTES)
            .ok_or(MessageTooLong)?;

        self.message_bytes = new_message_bytes;

        if self.buffered != 0 {
            let copied = (BLOCK_BYTES - self.buffered).min(input.len());
            self.buffer[self.buffered..self.buffered + copied].copy_from_slice(&input[..copied]);
            self.buffered += copied;
            input = &input[copied..];

            if self.buffered == BLOCK_BYTES {
                compress_block(&mut self.state, &self.buffer);
                self.buffered = 0;
            } else {
                return Ok(());
            }
        }

        while input.len() >= BLOCK_BYTES {
            let (block, rest) = input.split_at(BLOCK_BYTES);
            let block: &[u8; BLOCK_BYTES] = block
                .try_into()
                .expect("a slice split at BLOCK_BYTES has the required length");
            compress_block(&mut self.state, block);
            input = rest;
        }

        self.buffer[..input.len()].copy_from_slice(input);
        self.buffered = input.len();
        Ok(())
    }

    /// Finishes the hash and returns the 32-byte digest.
    #[must_use]
    pub fn finalize(mut self) -> [u8; 32] {
        let message_bits = self
            .message_bytes
            .checked_mul(8)
            .expect("update enforces the SHA-256 message-length limit");

        self.buffer[self.buffered] = 0x80;
        self.buffered += 1;

        if self.buffered > LENGTH_FIELD_OFFSET {
            self.buffer[self.buffered..].fill(0);
            compress_block(&mut self.state, &self.buffer);
            self.buffer = [0; BLOCK_BYTES];
        } else {
            self.buffer[self.buffered..LENGTH_FIELD_OFFSET].fill(0);
        }

        self.buffer[LENGTH_FIELD_OFFSET..].copy_from_slice(&message_bits.to_be_bytes());
        compress_block(&mut self.state, &self.buffer);

        let mut digest = [0; 32];
        for (word, output) in self.state.iter().zip(digest.chunks_exact_mut(4)) {
            output.copy_from_slice(&word.to_be_bytes());
        }
        digest
    }
}

impl Default for Sha256 {
    fn default() -> Self {
        Self::new()
    }
}

/// Hashes a byte slice in one call.
pub fn sha256_bytes(input: &[u8]) -> Result<[u8; 32], MessageTooLong> {
    let mut hasher = Sha256::new();
    hasher.update(input)?;
    Ok(hasher.finalize())
}

/// Hashes all bytes from `reader`, rejecting inputs larger than `max_bytes`.
///
/// A one-byte lookahead distinguishes an input exactly at the limit from one
/// that exceeds it. A configured limit above SHA-256's representable maximum
/// is rejected with [`io::ErrorKind::InvalidInput`].
pub fn sha256_reader<R: Read>(mut reader: R, max_bytes: u64) -> io::Result<[u8; 32]> {
    if max_bytes > MAX_MESSAGE_BYTES {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            "SHA-256 byte limit exceeds the algorithm's maximum message length",
        ));
    }

    let mut hasher = Sha256::new();
    let mut total = 0_u64;
    let mut buffer = [0_u8; 64 * 1024];

    loop {
        let remaining = max_bytes - total;
        if remaining == 0 {
            let mut lookahead = [0_u8; 1];
            let count = read_retry_interrupted(&mut reader, &mut lookahead)?;
            if count == 0 {
                return Ok(hasher.finalize());
            }
            return Err(io::Error::new(
                io::ErrorKind::InvalidData,
                format!("input exceeds the configured {max_bytes}-byte SHA-256 limit"),
            ));
        }

        let requested = remaining.min(buffer.len() as u64) as usize;
        let count = read_retry_interrupted(&mut reader, &mut buffer[..requested])?;
        if count == 0 {
            return Ok(hasher.finalize());
        }

        total = total
            .checked_add(u64::try_from(count).map_err(|_| {
                io::Error::new(
                    io::ErrorKind::InvalidData,
                    "reader byte count overflowed u64",
                )
            })?)
            .ok_or_else(|| {
                io::Error::new(
                    io::ErrorKind::InvalidData,
                    "reader byte count overflowed u64",
                )
            })?;
        hasher
            .update(&buffer[..count])
            .map_err(|error| io::Error::new(io::ErrorKind::InvalidData, error))?;
    }
}

/// Formats bytes as lowercase hexadecimal without separators.
#[must_use]
pub fn lowercase_hex(bytes: &[u8]) -> String {
    const DIGITS: &[u8; 16] = b"0123456789abcdef";

    let mut output = String::with_capacity(bytes.len().saturating_mul(2));
    for &byte in bytes {
        output.push(char::from(DIGITS[usize::from(byte >> 4)]));
        output.push(char::from(DIGITS[usize::from(byte & 0x0f)]));
    }
    output
}

fn read_retry_interrupted<R: Read>(reader: &mut R, buffer: &mut [u8]) -> io::Result<usize> {
    loop {
        match reader.read(buffer) {
            Err(error) if error.kind() == io::ErrorKind::Interrupted => {}
            result => return result,
        }
    }
}

fn compress_block(state: &mut [u32; 8], block: &[u8; BLOCK_BYTES]) {
    let mut schedule = [0_u32; 64];
    for (word, bytes) in schedule[..16].iter_mut().zip(block.chunks_exact(4)) {
        *word = u32::from_be_bytes(
            bytes
                .try_into()
                .expect("four-byte chunks have the required length"),
        );
    }

    for index in 16..64 {
        let sigma0 = schedule[index - 15].rotate_right(7)
            ^ schedule[index - 15].rotate_right(18)
            ^ (schedule[index - 15] >> 3);
        let sigma1 = schedule[index - 2].rotate_right(17)
            ^ schedule[index - 2].rotate_right(19)
            ^ (schedule[index - 2] >> 10);
        schedule[index] = schedule[index - 16]
            .wrapping_add(sigma0)
            .wrapping_add(schedule[index - 7])
            .wrapping_add(sigma1);
    }

    let [mut a, mut b, mut c, mut d, mut e, mut f, mut g, mut h] = *state;

    for index in 0..64 {
        let sum1 = e.rotate_right(6) ^ e.rotate_right(11) ^ e.rotate_right(25);
        let choose = (e & f) ^ ((!e) & g);
        let temporary1 = h
            .wrapping_add(sum1)
            .wrapping_add(choose)
            .wrapping_add(ROUND_CONSTANTS[index])
            .wrapping_add(schedule[index]);
        let sum0 = a.rotate_right(2) ^ a.rotate_right(13) ^ a.rotate_right(22);
        let majority = (a & b) ^ (a & c) ^ (b & c);
        let temporary2 = sum0.wrapping_add(majority);

        h = g;
        g = f;
        f = e;
        e = d.wrapping_add(temporary1);
        d = c;
        c = b;
        b = a;
        a = temporary1.wrapping_add(temporary2);
    }

    state[0] = state[0].wrapping_add(a);
    state[1] = state[1].wrapping_add(b);
    state[2] = state[2].wrapping_add(c);
    state[3] = state[3].wrapping_add(d);
    state[4] = state[4].wrapping_add(e);
    state[5] = state[5].wrapping_add(f);
    state[6] = state[6].wrapping_add(g);
    state[7] = state[7].wrapping_add(h);
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Cursor;

    fn digest_hex(input: &[u8]) -> String {
        lowercase_hex(&sha256_bytes(input).expect("test input fits SHA-256"))
    }

    #[test]
    fn empty_standard_vector() {
        assert_eq!(
            digest_hex(b""),
            "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
        );
    }

    #[test]
    fn abc_standard_vector() {
        assert_eq!(
            digest_hex(b"abc"),
            "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
        );
    }

    #[test]
    fn long_standard_vector() {
        let message = b"abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq";
        assert_eq!(
            digest_hex(message),
            "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1"
        );
    }

    #[test]
    fn chunked_updates_equal_one_shot() {
        let input: Vec<u8> = (0_u32..10_000)
            .flat_map(|number| number.to_le_bytes())
            .collect();
        let expected = sha256_bytes(&input).expect("test input fits SHA-256");

        for chunk_size in [1, 3, 7, 55, 56, 63, 64, 65, 127, 1_024] {
            let mut hasher = Sha256::new();
            for chunk in input.chunks(chunk_size) {
                hasher.update(chunk).expect("test input fits SHA-256");
            }
            assert_eq!(hasher.finalize(), expected, "chunk size {chunk_size}");
        }
    }

    #[test]
    fn million_a_standard_vector() {
        let mut hasher = Sha256::new();
        let chunk = [b'a'; 1_000];
        for _ in 0..1_000 {
            hasher.update(&chunk).expect("test input fits SHA-256");
        }
        assert_eq!(
            lowercase_hex(&hasher.finalize()),
            "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0"
        );
    }

    #[test]
    fn reader_hashes_exact_limit_and_rejects_excess() {
        let input = b"bounded reader";
        let expected = sha256_bytes(input).expect("test input fits SHA-256");
        assert_eq!(
            sha256_reader(Cursor::new(input), input.len() as u64).expect("exact limit is valid"),
            expected
        );

        let error = sha256_reader(Cursor::new(input), (input.len() - 1) as u64)
            .expect_err("input over the byte limit must fail");
        assert_eq!(error.kind(), io::ErrorKind::InvalidData);
    }

    #[test]
    fn update_length_error_is_atomic() {
        let mut hasher = Sha256::new();
        hasher.message_bytes = MAX_MESSAGE_BYTES;
        let before = hasher.clone();

        assert_eq!(hasher.update(b"x"), Err(MessageTooLong));
        assert_eq!(hasher.state, before.state);
        assert_eq!(hasher.buffer, before.buffer);
        assert_eq!(hasher.buffered, before.buffered);
        assert_eq!(hasher.message_bytes, before.message_bytes);
    }
}
