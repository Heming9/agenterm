//! Shared connection lifecycle for plain output and the interactive UI.
use crate::{
    protocol::{self, Address, Result, Token},
    storage,
    terminal::Manager,
    transport,
    ui::{Event, Reporter},
};
use std::sync::{
    Arc,
    atomic::{AtomicBool, Ordering},
};

pub fn short_target(address: &Address) -> Address {
    let mut target = address.clone();
    target.scheme = if address.secure() { "wss" } else { "ws" }.into();
    target.path = address.path.rsplit_once("/c/").unwrap().0.to_string() + "/client";
    target
}

pub fn display_address(value: &str) -> Result<String> {
    if value.starts_with("Agenterm_") {
        Ok(Token::decode(value)?.connection_url())
    } else {
        Ok(short_target(&protocol::short_address(value)?).url())
    }
}

pub fn run(
    value: &str,
    invited: bool,
    temporary: bool,
    stopped: Arc<AtomicBool>,
    reporter: Reporter,
) -> Result<()> {
    if stopped.load(Ordering::SeqCst) {
        return Ok(());
    }
    let (token, reservation) = if invited {
        let invitation = Token::decode(value)?;
        let reservation = storage::lock(&invitation.address)?;
        if stopped.load(Ordering::SeqCst) {
            return Ok(());
        }
        let token = transport::redeem(&invitation.invitation_url(), Some(&invitation.secret))?;
        if token.address.url() != invitation.address.url() {
            return Err("invitation resolved to a different server".into());
        }
        reporter.send(Event::Redeemed(token.clone()));
        (token, reservation)
    } else if value.starts_with("Agenterm_") {
        let token = Token::decode(value)?;
        let reservation = storage::lock(&token.address)?;
        (token, reservation)
    } else {
        let address = protocol::short_address(value)?;
        let target = short_target(&address);
        let reservation = storage::lock(&target)?;
        if stopped.load(Ordering::SeqCst) {
            return Ok(());
        }
        let token = transport::redeem(&address, None)?;
        if token.address.url() != target.url() {
            return Err("short code resolved to a different server".into());
        }
        reporter.send(Event::Redeemed(token.clone()));
        (token, reservation)
    };
    if stopped.load(Ordering::SeqCst) {
        return Ok(());
    }
    let manager = Manager::with_reporter(stopped.clone(), reporter);
    let result = transport::run(&token, &manager, &stopped, temporary, invited);
    manager.close();
    drop(reservation);
    result
}
