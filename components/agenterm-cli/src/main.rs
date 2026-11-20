mod connection;
mod deeplink;
mod p2p;
mod protocol;
mod storage;
mod terminal;
mod transport;
mod tui;
mod ui;

use protocol::Result;
use std::{
    io::{self, IsTerminal},
    sync::{
        Arc,
        atomic::{AtomicBool, Ordering},
    },
};

fn run() -> Result<()> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let link_launch = deeplink::is_launch(&args);
    let args = deeplink::arguments(args)?;
    let options = ui::Options::parse(args)?;
    let Some(mut options) = options else {
        return Ok(());
    };
    let register_link = !link_launch
        && std::env::var_os("AGENTERM_SKIP_LINK_REGISTRATION").as_deref()
            != Some(std::ffi::OsStr::new("1"));
    let startup_target = !options.token.is_empty() && !protocol::short_code(&options.token);
    let mut authorized = None;
    if startup_target {
        let address = connection::target_address(&options.token)?;
        authorized = ui::consent(&address.server(), options.mode)?;
        if authorized.is_none() {
            ui::line("Connection cancelled.", "已取消连接。");
            return Ok(());
        }
    }
    let stopped = Arc::new(AtomicBool::new(false));
    if !options.plain && io::stdin().is_terminal() && io::stdout().is_terminal() {
        install_signal(stopped.clone())?;
        return tui::run(options, stopped, authorized, register_link);
    }
    ui::heading();
    let mode = if let Some(mode) = authorized {
        mode
    } else {
        if options.token.is_empty() && !options.token_provided {
            options.token = ui::input(ui::text(
                "Connection token, short code, or short-code address: ",
                "连接 Token / 短码：",
            ))?;
        }
        if protocol::short_code(&options.token) {
            let servers = storage::history()?;
            if servers.is_empty() {
                return Err(ui::tr("history.server.empty").into());
            }
            println!("{}", ui::tr("select.history.server"));
            for (i, s) in servers.iter().enumerate() {
                println!("{}. {s}", i + 1);
            }
            let n = ui::input(ui::text("Server number: ", "服务器编号："))?
                .parse::<usize>()
                .map_err(|_| "invalid server number")?;
            let server = servers
                .get(n.checked_sub(1).ok_or("invalid server number")?)
                .ok_or("invalid server number")?;
            options.token = format!("{server}/c/{}", options.token.to_ascii_uppercase());
        }
        let address = connection::target_address(&options.token)?;
        let Some(mode) = ui::consent(&address.server(), options.mode)? else {
            ui::line("Connection cancelled.", "已取消连接。");
            return Ok(());
        };
        mode
    };
    install_signal(stopped.clone())?;
    connection::run(
        &options.token,
        mode,
        stopped,
        ui::Reporter::default(),
        register_link,
    )
}

fn install_signal(stopped: Arc<AtomicBool>) -> Result<()> {
    ctrlc::set_handler(move || stopped.store(true, Ordering::SeqCst)).map_err(|e| e.to_string())
}

fn main() {
    if let Err(e) = run() {
        eprintln!("[Agenterm] {e}");
        std::process::exit(1);
    }
}
