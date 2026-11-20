//! Shared connection lifecycle for plain output and the interactive UI.
use crate::{
    protocol::{self, Address, Result, Token},
    storage,
    terminal::Manager,
    transport,
    ui::{self, ConnectionMode, Event, Reporter},
};
use std::{
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

pub fn authorization(
    value: &str,
    requested: Option<ConnectionMode>,
) -> Result<(Address, Option<ConnectionMode>)> {
    let (target, temporary_only) = if value.starts_with("Agenterm_") {
        let token = Token::decode(value)?;
        (token.address, token.temporary_only)
    } else {
        let address = protocol::connection_address(value)?;
        (
            connection_target(&address),
            transport::invitation_temporary_only(&address)?,
        )
    };
    let allowed = if temporary_only {
        if requested == Some(ConnectionMode::Persistent) {
            return Err(ui::tr("connection.mode.temporary.required").into());
        }
        Some(ConnectionMode::Temporary)
    } else {
        requested
    };
    Ok((target, allowed))
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
    mode: ConnectionMode,
    stopped: Arc<AtomicBool>,
    reporter: Reporter,
    register_link: bool,
) -> Result<()> {
    let started = Instant::now();
    if stopped.load(Ordering::SeqCst) {
        return Ok(());
    }
    let temporary = mode == ConnectionMode::Temporary;
    let reserve = |address: &Address| (!temporary).then(|| storage::lock(address)).transpose();
    let (token, reservation, validation) = if value.starts_with("Agenterm_") {
        let token = Token::decode(value)?;
        if token.temporary_only && !temporary {
            return Err(ui::tr("connection.mode.temporary.required").into());
        }
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
        if token.temporary_only && !temporary {
            return Err(ui::tr("connection.mode.temporary.required").into());
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
    let workspace = temporary.then(storage::installer_workspace).flatten();
    let manager = Manager::with_reporter(stopped.clone(), reporter, temporary, workspace);
    let result = transport::run(&token, &manager, &stopped, started, register_link);
    manager.close();
    drop(reservation);
    result
}
