//! One length-prefixed framing, used everywhere this crate has to hand JS a
//! list of byte strings (key packages, group ids, member identities).
//!
//! `count: u32be, (len: u32be, bytes)*`. The alternative was `js_sys::Array`,
//! which would pull a crate in for something twenty lines cover, and JSON,
//! which would mean base64-ing every payload. `webapp/src/mls/bytes.ts` is the
//! other half — `packFrames`/`unpackFrames` — and the two must stay in step.

/// Packs a list of byte strings into one blob.
pub fn pack(items: &[Vec<u8>]) -> Vec<u8> {
    let mut out = Vec::new();
    out.extend_from_slice(&(items.len() as u32).to_be_bytes());
    for item in items {
        out.extend_from_slice(&(item.len() as u32).to_be_bytes());
        out.extend_from_slice(item);
    }
    out
}

/// Unpacks a blob written by [`pack`]. Any truncation or trailing byte is an
/// error rather than a best-effort read — a half-read list of key packages
/// would silently add fewer members than the caller asked for.
pub fn unpack(blob: &[u8]) -> Result<Vec<Vec<u8>>, &'static str> {
    let mut rest = blob;
    let count = take_u32(&mut rest)?;
    let mut items = Vec::with_capacity(count.min(1024));
    for _ in 0..count {
        let len = take_u32(&mut rest)?;
        if rest.len() < len {
            return Err("truncated frame");
        }
        let (head, tail) = rest.split_at(len);
        rest = tail;
        items.push(head.to_vec());
    }
    if !rest.is_empty() {
        return Err("trailing bytes after the last frame");
    }
    Ok(items)
}

fn take_u32(rest: &mut &[u8]) -> Result<usize, &'static str> {
    if rest.len() < 4 {
        return Err("truncated length prefix");
    }
    let (head, tail) = rest.split_at(4);
    *rest = tail;
    let bytes: [u8; 4] = head.try_into().map_err(|_| "truncated length prefix")?;
    Ok(u32::from_be_bytes(bytes) as usize)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips_including_the_empty_cases() {
        for items in [
            vec![],
            vec![vec![]],
            vec![vec![1u8, 2, 3], vec![], vec![9u8; 300]],
        ] {
            assert_eq!(unpack(&pack(&items)).unwrap(), items);
        }
    }

    #[test]
    fn refuses_truncated_and_trailing_input() {
        let blob = pack(&[vec![1, 2, 3]]);
        assert!(unpack(&blob[..blob.len() - 1]).is_err());
        assert!(unpack(&[blob.as_slice(), &[0]].concat()).is_err());
        assert!(unpack(&[]).is_err());
    }
}
