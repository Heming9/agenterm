use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD as B64};
use std::net::{Ipv4Addr, Ipv6Addr};

pub const VERSION: &str = env!("CARGO_PKG_VERSION");
pub const WIRE_VERSION: &str = env!("AGENTERM_WIRE_VERSION");
pub const CONNECTION_MODE_HEADER: &str = "x-agenterm-connection-mode";
pub const TEMPORARY_CONNECTION_MODE: &str = "temporary";
pub type Result<T> = std::result::Result<T, String>;

#[derive(Clone, Debug)]
pub struct Address {
    pub scheme: String,
    pub authority: String,
    pub host: String,
    pub port: u16,
    pub path: String,
}

impl Address {
    pub fn parse(raw: &str) -> Result<Self> {
        let (scheme, rest) = raw.split_once("://").ok_or("invalid server address")?;
        if !["http", "https", "ws", "wss"].contains(&scheme)
            || rest.bytes().any(|b| b <= 32 || b >= 127 || b == b'\\')
            || rest.contains(['@', '?', '#', '%'])
        {
            return Err("invalid server address".into());
        }
        let (authority, path) = rest
            .split_once('/')
            .map_or((rest, "/".to_string()), |(a, p)| (a, format!("/{p}")));
        let (host, port) = if authority.starts_with('[') {
            let end = authority.find(']').ok_or("invalid IPv6 address")?;
            let host = &authority[1..end];
            host.parse::<Ipv6Addr>()
                .map_err(|_| "invalid IPv6 address")?;
            let tail = &authority[end + 1..];
            (
                host,
                if tail.is_empty() {
                    None
                } else {
                    Some(tail.strip_prefix(':').ok_or("invalid port")?)
                },
            )
        } else {
            if authority.matches(':').count() > 1 {
                return Err("IPv6 addresses need brackets".into());
            }
            authority
                .rsplit_once(':')
                .map_or((authority, None), |(h, p)| (h, Some(p)))
        };
        if host.is_empty()
            || (!authority.starts_with('[')
                && !host
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'.'))
        {
            return Err("invalid hostname".into());
        }
        let port = match port {
            Some(p) => p
                .parse::<u16>()
                .ok()
                .filter(|p| *p > 0)
                .ok_or("invalid port")?,
            None => {
                if scheme == "https" || scheme == "wss" {
                    443
                } else {
                    80
                }
            }
        };
        Ok(Self {
            scheme: scheme.into(),
            authority: authority.into(),
            host: host.into(),
            port,
            path,
        })
    }
    pub fn secure(&self) -> bool {
        self.scheme == "https" || self.scheme == "wss"
    }
    pub fn url(&self) -> String {
        format!("{}://{}{}", self.scheme, self.authority, self.path)
    }
    pub fn server(&self) -> String {
        format!(
            "{}://{}{}",
            if self.secure() { "https" } else { "http" },
            self.authority,
            self.path.strip_suffix("/client").unwrap_or(&self.path)
        )
    }
}

#[derive(Clone, Debug)]
pub struct Token {
    pub encoded: String,
    pub address: Address,
    pub secret: String,
    pub identifier: String,
    pub temporary_only: bool,
}

impl Token {
    pub fn decode(raw: &str) -> Result<Self> {
        let mut payload = raw
            .trim()
            .strip_prefix("Agenterm_")
            .ok_or("invalid Agenterm connection token")?;
        let alphabet = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
        let head = payload
            .bytes()
            .next()
            .and_then(|c| alphabet.iter().position(|b| *b == c))
            .ok_or("empty token")?;
        let identifier = if head & 4 == 0 {
            let (p, id) = payload.rsplit_once('_').ok_or("missing token identifier")?;
            if id.len() != 6
                || !id
                    .bytes()
                    .all(|b| b.is_ascii_lowercase() || b.is_ascii_digit())
            {
                return Err("invalid token identifier".into());
            }
            payload = p;
            id.to_string()
        } else {
            String::new()
        };
        let mut escaped = String::new();
        let mut chars = payload.bytes();
        while let Some(b) = chars.next() {
            escaped.push(match b {
                b'_' => match chars.next() {
                    Some(b'0') => '_',
                    Some(b'1') => '-',
                    _ => return Err("invalid token escape".into()),
                },
                b if b.is_ascii_alphanumeric() => b as char,
                _ => return Err("invalid token character".into()),
            });
        }
        let data = B64.decode(escaped).map_err(|_| "invalid token encoding")?;
        if data.len() <= 11 {
            return Err("incomplete token".into());
        }
        let flags = data[0];
        if flags & 16 == 0 && identifier.is_empty() {
            return Err("unsupported token format".into());
        }
        let (scheme, temporary_only) = match flags & 3 {
            1 => ("ws", false),
            2 => ("wss", false),
            0 if !identifier.is_empty() => ("ws", true),
            3 if !identifier.is_empty() => ("wss", true),
            _ => return Err("invalid token scheme".into()),
        };
        let secret = B64.encode(&data[1..11]);
        let mut tail = &data[11..];
        let prefix = if flags & 32 != 0 {
            let n = take_u16(&mut tail)? as usize;
            if n > tail.len() {
                return Err("invalid path length".into());
            }
            let p = std::str::from_utf8(&tail[..n])
                .map_err(|_| "invalid path")?
                .to_string();
            tail = &tail[n..];
            validate_prefix(&p)?;
            p
        } else {
            "/agenterm".into()
        };
        let port = if flags & 64 != 0 {
            let p = take_u16(&mut tail)?;
            if p == 0 {
                return Err("invalid port".into());
            }
            format!(":{p}")
        } else {
            String::new()
        };
        let host = match flags & !(3 | 16 | 32 | 64) {
            0 => std::str::from_utf8(tail)
                .map_err(|_| "invalid hostname")?
                .to_string(),
            4 => Ipv4Addr::from(<[u8; 4]>::try_from(tail).map_err(|_| "invalid IPv4 token")?)
                .to_string(),
            8 => format!(
                "[{}]",
                Ipv6Addr::from(<[u8; 16]>::try_from(tail).map_err(|_| "invalid IPv6 token")?)
            ),
            128 => unpack_host(tail)?,
            _ => return Err("unsupported address format".into()),
        };
        let address = Address::parse(&format!("{scheme}://{host}{port}{prefix}/client"))?;
        Ok(Self {
            encoded: raw.trim().into(),
            address,
            secret,
            identifier,
            temporary_only,
        })
    }
    pub fn connection_url(&self) -> String {
        let url = self.address.url();
        if self.identifier.is_empty() {
            url
        } else {
            format!("{url}/{}", self.identifier)
        }
    }
}

fn take_u16(data: &mut &[u8]) -> Result<u16> {
    if data.len() < 2 {
        return Err("incomplete token".into());
    }
    let n = u16::from_be_bytes([data[0], data[1]]);
    *data = &data[2..];
    Ok(n)
}
pub fn validate_prefix(p: &str) -> Result<()> {
    if p.is_empty() {
        return Ok(());
    }
    if p.len() > 1024
        || !p.starts_with('/')
        || p[1..].split('/').any(|s| {
            s.is_empty()
                || s == "."
                || s == ".."
                || !s
                    .bytes()
                    .all(|b| b.is_ascii_alphanumeric() || b"-._~".contains(&b))
        })
    {
        return Err("invalid server path".into());
    }
    Ok(())
}
fn unpack_host(data: &[u8]) -> Result<String> {
    let alphabet = b"abcdefghijklmnopqrstuvwxyz0123456789-.";
    let mut bits = 0u32;
    let mut count = 0;
    let mut out = String::new();
    for (i, b) in data.iter().enumerate() {
        bits = (bits << 8) | *b as u32;
        count += 8;
        while count >= 6 {
            count -= 6;
            let n = (bits >> count) & 63;
            if n == 0 && i == data.len() - 1 && count == 0 {
                bits = 0;
                break;
            }
            if n == 0 || n > alphabet.len() as u32 {
                return Err("invalid packed hostname".into());
            }
            out.push(alphabet[n as usize - 1] as char);
            bits &= (1 << count) - 1;
        }
    }
    if bits != 0 || out.is_empty() {
        return Err("invalid hostname padding".into());
    }
    Ok(out)
}
pub fn short_code(raw: &str) -> bool {
    raw.len() == 6 && raw.bytes().all(|b| b.is_ascii_alphanumeric())
}
pub fn connection_address(raw: &str) -> Result<Address> {
    let a = Address::parse(&if raw.contains("://") {
        raw.into()
    } else {
        // The server omits the scheme only for HTTP connection URLs.
        format!("http://{raw}")
    })?;
    if !["http", "https"].contains(&a.scheme.as_str()) {
        return Err("expected a connection HTTP address".into());
    }
    let (prefix, code) = a
        .path
        .rsplit_once("/c/")
        .ok_or("invalid connection address")?;
    validate_prefix(prefix)?;
    let token_secret = code.len() == 14
        && code
            .bytes()
            .all(|b| b.is_ascii_alphanumeric() || b == b'-' || b == b'_');
    if !short_code(code) && !token_secret {
        return Err("invalid connection credential".into());
    }
    Ok(a)
}
pub fn check_version(peer: &str) -> Result<()> {
    fn split(v: &str) -> Option<(u32, u32, u32, &str)> {
        let v = v.strip_prefix('v').unwrap_or(v).split('+').next()?;
        let (n, pre) = v.split_once('-').unwrap_or((v, ""));
        let mut n = n.split('.');
        let result = (
            n.next()?.parse().ok()?,
            n.next()?.parse().ok()?,
            n.next()?.parse().ok()?,
            pre,
        );
        if n.next().is_some() {
            None
        } else {
            Some(result)
        }
    }
    let a = split(WIRE_VERSION).ok_or("invalid local protocol version")?;
    let b = split(peer).ok_or("server did not supply a compatible protocol version")?;
    if a.0 != b.0
        || (a.0 == 0 && a.1 != b.1)
        || (a.0 == 0 && a.1 == 0 && a.2 != b.2)
        || ((!a.3.is_empty() || !b.3.is_empty()) && (a != b))
    {
        Err(format!(
            "protocol version mismatch: client {WIRE_VERSION}, server {peer}"
        ))
    } else {
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn short_code_addresses_follow_the_server_scheme_convention() {
        for (input, expected) in [
            ("example.com/c/abc123", "http://example.com/c/abc123"),
            (
                "[::1]:8080/test/c/abc123",
                "http://[::1]:8080/test/c/abc123",
            ),
            (
                "https://example.com/c/abc123",
                "https://example.com/c/abc123",
            ),
            ("http://example.com/c/abc123", "http://example.com/c/abc123"),
        ] {
            assert_eq!(connection_address(input).unwrap().url(), expected);
        }
    }
    #[test]
    fn token_ipv4() {
        let mut b = vec![1 | 4 | 64];
        b.extend(0..10);
        b.extend(8090u16.to_be_bytes());
        b.extend([127, 0, 0, 1]);
        let payload = B64.encode(b).replace('_', "_0").replace('-', "_1");
        let t = Token::decode(&format!("Agenterm_{payload}_abc123")).unwrap();
        assert_eq!(
            t.connection_url(),
            "ws://127.0.0.1:8090/agenterm/client/abc123"
        );
    }
    #[test]
    fn reject_urls() {
        for s in [
            "https://x@evil/",
            "https://x/\r\nAuthorization:x",
            "https://x/a?b",
            "https://x/%2f",
        ] {
            assert!(Address::parse(s).is_err());
        }
    }
    #[test]
    fn versions() {
        assert!(check_version(WIRE_VERSION).is_ok());
        assert!(check_version("0.999.0").is_err());
    }
}
