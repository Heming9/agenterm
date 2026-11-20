//! Shared connection lifecycle for plain output and the interactive UI.
use crate::{
    protocol::{self, Address, Result, Token},
    storage,
    terminal::Manager,
    transport,
    ui::{Event, Reporter},
};
use std::{
    path::PathBuf,
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
    time::Instant,
};

pub fn connection_target(address: &Address) -> Address {
    let mut target = address.clone();
    target.scheme = if address.secure() { "wss" } else { "ws" }.into();
    target.path = address.path.rsplit_once("/c/").unwrap().0.to_string() + "/client";
    target
}

pub fn target_address(value: &str) -> Result<Address> {
    if value.starts_with("Agenterm_") {
        Ok(Token::decode(value)?.address)
    } else {
        Ok(connection_target(&protocol::connection_address(value)?))
    }
}

pub fn display_address(value: &str) -> Result<String> {
    if value.starts_with("Agenterm_") {
        Ok(Token::decode(value)?.connection_url())
    } else {
        Ok(target_address(value)?.url())
    }
}

pub fn run(
    value: &str,
    temporary: bool,
    stopped: Arc<AtomicBool>,
    reporter: Reporter,
    workspace: Option<PathBuf>,
) -> Result<()> {
    let started = Instant::now();
    if stopped.load(Ordering::SeqCst) {
        return Ok(());
    }
    // A temporary installer launches a single connection and the server
    // already arbitrates token admission. The reusable CLI also reserves a
    // per-server lock across its local instances.
    let reserve = |address: &Address| {
        workspace
            .is_none()
            .then(|| storage::lock(address))
            .transpose()
    };
    let (token, reservation, validation) = if value.starts_with("Agenterm_") {
        let token = Token::decode(value)?;
        let reservation = reserve(&token.address)?;
        (token, reservation, None)
    } else {
        let address = protocol::connection_address(value)?;
        let target = connection_target(&address);
        let reservation = reserve(&target)?;
        if stopped.load(Ordering::SeqCst) {
            return Ok(());
        }
        let validation_started = Instant::now();
        let (token, server_version) = transport::redeem(&address)?;
        if token.address.url() != target.url() {
            return Err("connection URL resolved to a different server".into());
        }
        (
            token,
            reservation,
            Some((server_version, validation_started.elapsed())),
        )
    };
    if !stopped.load(Ordering::SeqCst) {
        reporter.connecting(&token);
    }
    if let Some((server_version, elapsed)) = validation {
        reporter.send(Event::Redeemed {
            token: token.clone(),
            server_version,
            elapsed,
        });
    }
    if stopped.load(Ordering::SeqCst) {
        return Ok(());
    }
    let manager = Manager::with_reporter(stopped.clone(), reporter, workspace);
    let result = transport::run(&token, &manager, &stopped, temporary, started);
    manager.close();
    drop(reservation);
    result
}
