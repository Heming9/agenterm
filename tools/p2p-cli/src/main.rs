use std::{
    env,
    error::Error,
    io::ErrorKind,
    net::{IpAddr, Ipv4Addr, SocketAddr, UdpSocket},
    time::{Duration, Instant},
};
use str0m::{Candidate, CandidateKind};
#[cfg(windows)]
mod background_windows;
mod daemon;
mod signaling;
mod transfer;
mod transfer_file;

fn main() {
    if let Err(e) = run() {
        eprintln!("p2p-cli: {e}");
        std::process::exit(1);
    }
}

const USAGE: &str = "p2p-cli start --config FILE|-\np2p-cli status [--verbose|-v]\np2p-cli cp CLIENT:PATH CLIENT:PATH [--overwrite]\np2p-cli stop";

fn run() -> Result<(), Box<dyn Error>> {
    match env::args().nth(1).as_deref() {
        None | Some("--help" | "-h") => {
            println!("{USAGE}");
            Ok(())
        }
        Some("start" | "status" | "cp" | "stop" | "_serve") => daemon::run(),
        Some(command) => Err(format!("unknown command: {command}\n{USAGE}").into()),
    }
}

// str0m 0.24.1's SDP parser accepts candidate extensions only in a fixed
// order. Pion emits `ufrag ... generation 0`, which it silently discards.
// Its standalone candidate parser accepts arbitrary extension ordering;
// serialize candidates in the canonical order before parsing the full SDP.
fn normalize_candidates(sdp: &str) -> Result<String, Box<dyn Error>> {
    let mut result = String::with_capacity(sdp.len());
    let mut count = 0;
    for line in sdp.lines() {
        if let Some(candidate) = line.strip_prefix("a=candidate:") {
            let parsed = Candidate::from_sdp_string(&format!("candidate:{candidate}"))?;
            if parsed.kind() == CandidateKind::Relayed {
                return Err("relay candidate rejected".into());
            }
            result.push_str("a=");
            result.push_str(&parsed.to_sdp_string());
            count += 1;
        } else {
            result.push_str(line);
        }
        result.push_str("\r\n");
    }
    if count == 0 {
        return Err("peer SDP has no ICE candidates".into());
    }
    Ok(result)
}

fn stun_address_until(
    socket: &UdpSocket,
    server: SocketAddr,
    canceled: impl Fn() -> bool,
) -> Result<SocketAddr, Box<dyn Error>> {
    let mut request = [0u8; 20];
    request[0..2].copy_from_slice(&1u16.to_be_bytes());
    request[4..8].copy_from_slice(&0x2112a442u32.to_be_bytes());
    getrandom::fill(&mut request[8..20])?;
    socket.set_read_timeout(Some(Duration::from_millis(100)))?;
    let mut buf = [0u8; 1500];
    for _ in 0..3 {
        let deadline = Instant::now() + Duration::from_secs(2);
        socket.send_to(&request, server)?;
        loop {
            if canceled() {
                return Err("canceled".into());
            }
            if Instant::now() >= deadline {
                break;
            }
            match socket.recv_from(&mut buf) {
                Ok((n, source))
                    if source == server
                        && n >= 20
                        && buf[0..2] == [1, 1]
                        && buf[4..20] == request[4..20] =>
                {
                    let end = (20 + u16::from_be_bytes([buf[2], buf[3]]) as usize).min(n);
                    let mut i = 20;
                    while i + 4 <= end {
                        let typ = u16::from_be_bytes([buf[i], buf[i + 1]]);
                        let len = u16::from_be_bytes([buf[i + 2], buf[i + 3]]) as usize;
                        let start = i + 4;
                        if start + len > end {
                            return Err("malformed STUN response".into());
                        }
                        if typ == 0x0020 && len >= 8 && buf[start + 1] == 1 {
                            let port =
                                u16::from_be_bytes([buf[start + 2], buf[start + 3]]) ^ 0x2112;
                            let raw = u32::from_be_bytes(buf[start + 4..start + 8].try_into()?)
                                ^ 0x2112a442;
                            return Ok(SocketAddr::new(IpAddr::V4(Ipv4Addr::from(raw)), port));
                        }
                        i = start + ((len + 3) & !3);
                    }
                    return Err("STUN response lacks IPv4 XOR-MAPPED-ADDRESS".into());
                }
                Ok(_) => continue,
                Err(e) if e.kind() == ErrorKind::TimedOut || e.kind() == ErrorKind::WouldBlock => {
                    continue;
                }
                Err(e) => return Err(e.into()),
            }
        }
    }
    Err("STUN request timed out".into())
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::thread;
    use str0m::change::SdpAnswer;

    #[test]
    fn normalizes_pion_candidate_extension_order() {
        let sdp = "v=0\r\no=- 1 1 IN IP4 0.0.0.0\r\ns=-\r\nt=0 0\r\na=group:BUNDLE 0\r\nm=application 9 UDP/DTLS/SCTP webrtc-datachannel\r\nc=IN IP4 0.0.0.0\r\na=mid:0\r\na=ice-ufrag:test\r\na=ice-pwd:012345678901234567890123\r\na=candidate:123 1 udp 2130706431 192.0.2.10 5000 typ host ufrag test generation 0\r\n";
        let fixed = normalize_candidates(sdp).unwrap();
        assert!(
            fixed.contains(
                "a=candidate:123 1 udp 2130706431 192.0.2.10 5000 typ host ufrag test\r\n"
            )
        );
        assert!(!fixed.contains("generation"));
        let answer = SdpAnswer::from_sdp_string(&fixed).unwrap();
        assert!(answer.to_sdp_string().contains("a=candidate:123"));
    }

    #[test]
    fn rejects_relay_candidate() {
        let sdp = "v=0\r\na=candidate:1 1 udp 1 192.0.2.10 5000 typ relay\r\n";
        assert!(
            normalize_candidates(sdp)
                .unwrap_err()
                .to_string()
                .contains("relay")
        );
    }

    #[test]
    fn decodes_stun_xor_mapped_ipv4() {
        let server = UdpSocket::bind("127.0.0.1:0").unwrap();
        let addr = server.local_addr().unwrap();
        let worker = thread::spawn(move || {
            let mut request = [0u8; 20];
            let (n, peer) = server.recv_from(&mut request).unwrap();
            assert_eq!(n, 20);
            let mut response = [0u8; 32];
            response[0..2].copy_from_slice(&0x0101u16.to_be_bytes());
            response[2..4].copy_from_slice(&12u16.to_be_bytes());
            response[4..20].copy_from_slice(&request[4..20]);
            response[20..22].copy_from_slice(&0x0020u16.to_be_bytes());
            response[22..24].copy_from_slice(&8u16.to_be_bytes());
            response[25] = 1;
            response[26..28].copy_from_slice(&(12345u16 ^ 0x2112).to_be_bytes());
            response[28..32].copy_from_slice(
                &(u32::from(Ipv4Addr::new(203, 0, 113, 7)) ^ 0x2112a442).to_be_bytes(),
            );
            server.send_to(&response, peer).unwrap();
        });
        let client = UdpSocket::bind("127.0.0.1:0").unwrap();
        assert_eq!(
            stun_address_until(&client, addr, || false).unwrap(),
            "203.0.113.7:12345".parse().unwrap()
        );
        worker.join().unwrap();
    }
}
